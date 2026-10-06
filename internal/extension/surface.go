package extension

import (
	"cmp"
	"encoding/json"
	"fmt"
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
	// Key is the thing itself, exactly: of a kind, two items of one key are
	// the same, and one changed in any way is another.
	Key string `json:"key"`
	// Detail tells the thing in full for the user to read, as a terminal is
	// to show it.
	Detail string `json:"-"`
}

// Standing is where the user stands on a surface: what of it they agreed
// to, in effect, and what they declined.
type Standing struct {
	Surface, Agreed, Declined Surface
}

// Held returns what of the surface is not in effect.
func (s Standing) Held() Surface { return s.Surface.Missing(s.Agreed) }

// Ask returns what of the surface the user has yet to decide on.
func (s Standing) Ask() Surface { return s.Held().Missing(s.Declined) }

func (it Item) same(other Item) bool { return it.Kind == other.Kind && it.Key == other.Key }

// With returns s with items, sorted.
func (s Surface) With(items ...Item) Surface { return sorted(slices.Concat(s, items)) }

// Has reports whether s holds it.
func (s Surface) Has(it Item) bool { return slices.ContainsFunc(s, it.same) }

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

// NewItem makes an item of the string it is: a rule, a root, a source.
func NewItem(kind, s string) Item { return Item{kind, s, printable.Escape(s)} }

// valueItem makes an item of v, told by detail.
func valueItem(kind string, v any, detail string) Item {
	key, err := json.Marshal(v)
	if err != nil {
		panic(err) // v is plain data
	}
	return Item{kind, string(key), printable.Escape(detail)}
}

func sorted(s Surface) Surface {
	slices.SortFunc(s, func(a, b Item) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Key, b.Key))
	})
	return slices.CompactFunc(s, Item.same)
}

// grantItem is one of a project's grants: its item, and how it takes
// effect.
type grantItem struct {
	Item
	apply func(*config.Settings)
}

// grantItems lists a project's grants, config.ForProject's, one item each.
func grantItems(g config.Settings) []grantItem {
	var out []grantItem
	add := func(it Item, apply func(*config.Settings)) { out = append(out, grantItem{it, apply}) }
	for _, h := range hooksOf(Project, g.Hooks) {
		add(h.item(), func(s *config.Settings) { s.Hooks[h.Event] = append(s.Hooks[h.Event], h.HookEntry) })
	}
	for name, srv := range g.MCPServers {
		add(MCPServer{Name: name, MCPServer: srv}.item(), func(s *config.Settings) { s.MCPServers[name] = srv })
	}
	for _, raw := range g.Plugins {
		add(NewItem("plugin", raw), func(s *config.Settings) { s.Plugins = append(s.Plugins, raw) })
	}
	if p := g.Permissions; p != nil {
		for _, r := range p.Allow {
			add(NewItem("allow", r), func(s *config.Settings) { s.Permissions.Allow = append(s.Permissions.Allow, r) })
		}
		for _, r := range p.ReadRoots {
			add(NewItem("read", r), func(s *config.Settings) { s.Permissions.ReadRoots = append(s.Permissions.ReadRoots, r) })
		}
		for _, r := range p.WriteRoots {
			add(NewItem("write", r), func(s *config.Settings) { s.Permissions.WriteRoots = append(s.Permissions.WriteRoots, r) })
		}
	}
	return out
}

// projectSurface returns the surface of a project of grants and skills.
func projectSurface(grants []grantItem, skills []skill.Spec) Surface {
	var s Surface
	for _, g := range grants {
		s = append(s, g.Item)
	}
	for _, spec := range skills {
		s = append(s, skillItems(spec.Name, spec)...)
	}
	return sorted(s)
}

// grant returns the settings of the grants the user agreed to.
func grant(grants []grantItem, agreed Surface) config.Settings {
	out := config.Settings{Hooks: config.HooksConfig{}, MCPServers: map[string]config.MCPServer{}, Permissions: &config.PermissionsConfig{}}
	for _, g := range grants {
		if agreed.Has(g.Item) {
			g.apply(&out)
		}
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
	detail := h.Detail()
	if h.Plugin != "" {
		detail = h.Plugin + ": " + detail
	}
	return valueItem("hook", struct {
		Event  string `json:"event"`
		Plugin string `json:"plugin,omitempty"`
		config.HookEntry
	}{h.Event, h.Plugin, h.HookEntry}, detail)
}

func (srv MCPServer) item() Item {
	return valueItem("mcp", struct {
		Name string `json:"name"`
		config.MCPServer
	}{srv.Name, srv.MCPServer}, srv.Detail())
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
	if h.Blocking != nil && *h.Blocking {
		b.WriteString(" blocking")
	}
	if h.Timeout != nil {
		fmt.Fprintf(&b, " timeout %ds", *h.Timeout)
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
