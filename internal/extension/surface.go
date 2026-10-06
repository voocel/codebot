package extension

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/voocel/codebot/internal/agent/skill"
	"github.com/voocel/codebot/internal/infra/config"
)

// Surface is what of a project runs code or lets calls through unasked, so
// takes effect only once the user trusts it: its hooks, MCP servers, allow
// rules and roots, and what its skills may do only where trusted. It is the
// thing itself, not a digest of it, so the user is asked about just what
// changed since they trusted it. Items are sorted.
type Surface []Item

// Item is one thing on a project's surface.
type Item struct {
	// Kind is "hook", "mcp", "allow", "read", "write", "skill" or "plugin".
	Kind string `json:"kind"`
	// Detail is what runs or goes through, in full: two items alike are the
	// same thing.
	Detail string `json:"detail"`
}

// With returns s with items, sorted.
func (s Surface) With(items Surface) Surface {
	return sorted(slices.Concat(s, items))
}

// Missing returns the items of s that trusted lacks.
func (s Surface) Missing(trusted Surface) Surface {
	var out Surface
	for _, it := range s {
		if !slices.Contains(trusted, it) {
			out = append(out, it)
		}
	}
	return out
}

// surface returns the surface of a project of settings p, skills and
// plugins: the sources of those it declares, and what those read run. A git
// plugin fetched once the project is trusted adds what it runs, so the user
// is asked about it before it does.
func surface(p config.Settings, skills []skill.Spec, plugins []Plugin) Surface {
	var s Surface
	add := func(kind, detail string) { s = append(s, NewItem(kind, detail)) }
	for _, h := range hooksOf(Project, p.Hooks) {
		add("hook", h.Detail())
	}
	for name, srv := range p.MCPServers {
		add("mcp", MCPServer{Name: name, MCPServer: srv}.Detail())
	}
	if perms := p.Permissions; perms != nil {
		for _, r := range perms.Allow {
			add("allow", r)
		}
		for _, r := range perms.ReadRoots {
			add("read", r)
		}
		for _, r := range perms.WriteRoots {
			add("write", r)
		}
	}
	for _, spec := range skills {
		for _, p := range spec.Privileges() {
			add("skill", spec.Name+" "+p)
		}
	}
	for _, raw := range p.Plugins {
		add("plugin", raw)
	}
	for _, pl := range plugins {
		if pl.Scope == Project && pl.Plugin != nil {
			s = append(s, PluginSurface(pl.Plugin)...)
		}
	}
	return sorted(s)
}

// NewItem makes an item, what in detail a terminal would act on rather
// than show escaped: what the user reads is all there is.
func NewItem(kind, detail string) Item {
	var b strings.Builder
	for _, r := range detail {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
			continue
		}
		q := strconv.QuoteRune(r)
		b.WriteString(q[1 : len(q)-1])
	}
	return Item{kind, b.String()}
}

func sorted(s Surface) Surface {
	slices.SortFunc(s, func(a, b Item) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Detail, b.Detail))
	})
	return slices.Compact(s)
}

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
