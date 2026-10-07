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

// Surface is the part of a project or plugin that runs code or skips
// permission prompts, so it needs the user's consent: hooks, MCP servers,
// allow rules, roots, plugins a project declares, and skill privileges. It
// stores the items themselves rather than a digest, so the user is asked
// only about items that are new. Items are sorted.
type Surface []Item

type Item struct {
	// Kind is "hook", "mcp", "allow", "read", "write", "skill" or "plugin".
	Kind string `json:"kind"`
	// Key is the full content of the item: any change makes it a different
	// item that needs consent again.
	Key string `json:"key"`
	// Detail is the escaped, human-readable form of the item.
	Detail string `json:"-"`
}

// Standing splits a surface into what the user agreed to and what they
// declined.
type Standing struct {
	Surface, Agreed, Declined Surface
}

// Held returns the items not in effect.
func (s Standing) Held() Surface { return s.Surface.Missing(s.Agreed) }

func (s Standing) Ask() Surface { return s.Held().Missing(s.Declined) }

func (it Item) same(other Item) bool { return it.Kind == other.Kind && it.Key == other.Key }

func (s Surface) With(items ...Item) Surface { return sorted(slices.Concat(s, items)) }

func (s Surface) Has(it Item) bool { return slices.ContainsFunc(s, it.same) }

func (s Surface) HasAll(items []Item) bool {
	return !slices.ContainsFunc(items, func(it Item) bool { return !s.Has(it) })
}

func (s Surface) Intersect(other Surface) Surface {
	var out Surface
	for _, it := range s {
		if other.Has(it) {
			out = append(out, it)
		}
	}
	return out
}

func (s Surface) Missing(agreed Surface) Surface {
	var out Surface
	for _, it := range s {
		if !agreed.Has(it) {
			out = append(out, it)
		}
	}
	return out
}

func NewItem(kind, s string) Item { return Item{kind, s, printable.Escape(s)} }

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

type grantItem struct {
	Item
	apply func(*config.Settings)
}

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

func grant(grants []grantItem, agreed Surface) config.Settings {
	out := config.Settings{Hooks: config.HooksConfig{}, MCPServers: map[string]config.MCPServer{}, Permissions: &config.PermissionsConfig{}}
	for _, g := range grants {
		if agreed.Has(g.Item) {
			g.apply(&out)
		}
	}
	return out
}

// skillItems lists a skill's privileges. They are all-or-nothing: the skill
// runs with all of them or with none.
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
		if h.CommandWindows != "" {
			b.WriteString(" · on Windows: " + h.CommandWindows)
		}
	}
	return b.String()
}

// Detail omits a plugin's PLUGIN_ROOT and PLUGIN_DATA, which codebot sets.
func (srv MCPServer) Detail() string {
	if srv.Type == "http" {
		detail := srv.Name + ": " + srv.URL + pairs(" header", srv.Headers)
		if srv.OAuth != nil {
			detail += " oauth client " + srv.OAuth.ClientID
		}
		return detail
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

func pairs(label string, m map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b.WriteString(label + " " + k + "=" + m[k])
	}
	return b.String()
}
