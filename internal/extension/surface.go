package extension

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/voocel/codebot/internal/agent/skill"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/lib/printable"
)

// Surface is what of a project or a plugin runs code or lets calls through
// unasked, so takes effect only once the user agrees to it: its hooks, MCP
// servers, allow rules and roots, the plugins a project declares, and what
// its skills may do only where agreed to. It is the thing itself, not a
// digest of it: the user agrees to each item, and is asked about just those
// new since they last did. Items are sorted.
type Surface []Item

// Item is one thing on a surface.
type Item struct {
	// Kind is "hook", "mcp", "allow", "read", "write", "skill" or "plugin".
	Kind string `json:"kind"`
	// Detail is what runs or goes through, in full: two items alike are the
	// same thing.
	Detail string `json:"detail"`
}

// With returns s with items, sorted.
func (s Surface) With(items ...Item) Surface { return sorted(slices.Concat(s, items)) }

// Has reports whether s holds it.
func (s Surface) Has(it Item) bool { return slices.Contains(s, it) }

// HasAll reports whether s holds each of items.
func (s Surface) HasAll(items []Item) bool {
	return !slices.ContainsFunc(items, func(it Item) bool { return !s.Has(it) })
}

// Intersect returns the items of s that other holds too.
func (s Surface) Intersect(other Surface) Surface {
	var out Surface
	for _, it := range s {
		if other.Has(it) {
			out = append(out, it)
		}
	}
	return out
}

// Missing returns the items of s that agreed lacks.
func (s Surface) Missing(agreed Surface) Surface {
	var out Surface
	for _, it := range s {
		if !agreed.Has(it) {
			out = append(out, it)
		}
	}
	return out
}

// NewItem makes an item, what in detail a terminal would act on rather
// than show escaped: what the user reads is all there is.
func NewItem(kind, detail string) Item {
	return Item{kind, printable.Escape(detail)}
}

func sorted(s Surface) Surface {
	slices.SortFunc(s, func(a, b Item) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Detail, b.Detail))
	})
	return slices.Compact(s)
}

// projectSurface returns the surface of a project of grants and skills: see
// config.ForProject.
func projectSurface(grants config.Settings, skills []skill.Spec) Surface {
	var s Surface
	for _, h := range hooksOf(Project, grants.Hooks) {
		s = append(s, h.item())
	}
	for name, srv := range grants.MCPServers {
		s = append(s, MCPServer{Name: name, MCPServer: srv}.item())
	}
	for _, raw := range grants.Plugins {
		s = append(s, NewItem("plugin", raw))
	}
	if p := grants.Permissions; p != nil {
		for kind, rules := range map[string][]string{"allow": p.Allow, "read": p.ReadRoots, "write": p.WriteRoots} {
			for _, r := range rules {
				s = append(s, NewItem(kind, r))
			}
		}
	}
	for _, spec := range skills {
		s = append(s, skillItems(spec.Name, spec)...)
	}
	return sorted(s)
}

// grant keeps of a project's grants those the user agreed to.
func grant(grants config.Settings, agreed Surface) config.Settings {
	var out config.Settings
	for _, h := range hooksOf(Project, grants.Hooks) {
		if agreed.Has(h.item()) {
			if out.Hooks == nil {
				out.Hooks = config.HooksConfig{}
			}
			out.Hooks[h.Event] = append(out.Hooks[h.Event], h.HookEntry)
		}
	}
	for name, srv := range grants.MCPServers {
		if agreed.Has(MCPServer{Name: name, MCPServer: srv}.item()) {
			if out.MCPServers == nil {
				out.MCPServers = map[string]config.MCPServer{}
			}
			out.MCPServers[name] = srv
		}
	}
	for _, raw := range grants.Plugins {
		if agreed.Has(NewItem("plugin", raw)) {
			out.Plugins = append(out.Plugins, raw)
		}
	}
	if p := grants.Permissions; p != nil {
		keep := func(kind string, rules []string) []string {
			return slices.DeleteFunc(slices.Clone(rules), func(r string) bool { return !agreed.Has(NewItem(kind, r)) })
		}
		out.Permissions = &config.PermissionsConfig{Allow: keep("allow", p.Allow), ReadRoots: keep("read", p.ReadRoots), WriteRoots: keep("write", p.WriteRoots)}
	}
	return out
}

// skillItems lists what the skill named name may do only where agreed to.
// They go together: it runs as a whole, or without its privileges.
func skillItems(name string, spec skill.Spec) []Item {
	var out []Item
	for _, p := range spec.Privileges() {
		out = append(out, NewItem("skill", name+" "+p))
	}
	return out
}

func (h Hook) item() Item {
	if h.Plugin != "" {
		return NewItem("hook", h.Plugin+": "+h.Detail())
	}
	return NewItem("hook", h.Detail())
}

func (srv MCPServer) item() Item { return NewItem("mcp", srv.Detail()) }

// Detail tells the hook as "Event(matcher) if …: what it runs".
func (h Hook) Detail() string {
	var b strings.Builder
	b.WriteString(h.Event)
	if h.Matcher != "" {
		b.WriteString("(" + h.Matcher + ")")
	}
	if h.If != "" {
		b.WriteString(" if " + h.If)
	}
	b.WriteString(": ")
	switch h.Type {
	case "prompt":
		b.WriteString("prompt " + strconv.Quote(h.Prompt))
	case "http":
		b.WriteString("POST " + h.URL + pairs(" header", h.Headers))
	default:
		b.WriteString(h.Command)
	}
	return b.String()
}

// Detail tells the server as "name: the command it runs, or the URL it
// calls". Of a plugin's environment, PLUGIN_ROOT and PLUGIN_DATA are
// codebot's, so left out.
func (srv MCPServer) Detail() string {
	if srv.Type == "http" {
		return srv.Name + ": " + srv.URL + pairs(" header", srv.Headers)
	}
	env := srv.Env
	if srv.Plugin != "" {
		env = maps.Clone(env)
		delete(env, "PLUGIN_ROOT")
		delete(env, "PLUGIN_DATA")
	}
	words := []string{srv.Command}
	for _, a := range srv.Args {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\") {
			a = strconv.Quote(a)
		}
		words = append(words, a)
	}
	detail := srv.Name + ": " + strings.Join(words, " ") + pairs(" env", env)
	if srv.Cwd != "" {
		detail += " in " + srv.Cwd
	}
	return detail
}

// pairs tells m as " label K=V", one per key, by key.
func pairs(label string, m map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b.WriteString(label + " " + k + "=" + m[k])
	}
	return b.String()
}
