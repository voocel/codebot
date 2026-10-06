package commands

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/ui/tui/editor"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// modelChoice is a model in /model, with the reasoning effort picked for it.
type modelChoice struct {
	provider, name string
	efforts        []string
	effort         int
}

func (c *modelChoice) detail(window int) string {
	var parts []string
	if window > 0 {
		parts = append(parts, transcript.Tokens(window)+" context")
	}
	if len(c.efforts) > 1 {
		e := c.efforts[c.effort]
		if e == "" {
			e = "auto"
		}
		parts = append(parts, "effort ◂ "+e+" ▸")
	}
	return strings.Join(parts, " · ")
}

func model(a *app.App) Command {
	return Command{Name: "model", Aliases: []string{"m"}, Description: "Switch the model and its reasoning effort", Idle: true, Run: func(string) tea.Cmd {
		st := a.Current().Status()
		providers := a.Settings().Providers
		var items []panel.Item
		for _, prov := range slices.Sorted(maps.Keys(providers)) {
			for _, name := range providers[prov].Models {
				c := &modelChoice{provider: prov, name: name, efforts: a.ThinkingLevels(prov, name)}
				c.effort = max(slices.Index(c.efforts, st.Effort), 0)
				window := 0
				if facts, ok := a.ModelFacts(prov, name); ok {
					window = facts.MaxInputTokens
				}
				items = append(items, panel.Item{
					Group:   prov,
					Title:   name,
					Detail:  c.detail(window),
					Current: prov == st.Provider && strings.EqualFold(name, st.Model),
					Value:   c,
				})
			}
		}
		if len(items) == 0 {
			return fail("No models configured: add them to a provider in " + transcript.ShortPath(config.UserSettingsPath()))
		}
		conv := a.Current()
		return show(&panel.List{
			Title:  "Model",
			Items:  items,
			Filter: true,
			Hint:   theme.Hint("↑↓", "select", "←→", "effort", "enter", "switch", "esc", "close"),
			Keys: func(k string, it *panel.Item) (tea.Cmd, bool, bool) {
				if it == nil || (k != "left" && k != "right") {
					return nil, false, false
				}
				c := it.Value.(*modelChoice)
				if n := len(c.efforts); n > 1 {
					step := 1
					if k == "left" {
						step = n - 1
					}
					c.effort = (c.effort + step) % n
					window := 0
					if facts, ok := a.ModelFacts(c.provider, c.name); ok {
						window = facts.MaxInputTokens
					}
					it.Detail = c.detail(window)
				}
				return nil, true, false
			},
			Select: func(it panel.Item) tea.Cmd {
				c := it.Value.(*modelChoice)
				effort := c.efforts[c.effort]
				if err := conv.SetModel(c.provider, c.name, effort); err != nil {
					return fail("Could not switch the model: " + err.Error())
				}
				msg := "Model: " + config.FormatModelID(c.provider, c.name)
				if effort != "" {
					msg += " · effort " + effort
				}
				return note(msg)
			},
		})
	}}
}

func settings(a *app.App) Command {
	return Command{Name: "settings", Aliases: []string{"config"}, Description: "Show the settings in effect", Run: func(string) tea.Cmd {
		s := a.Settings()
		st := a.Current().Status()
		pc := s.Providers[st.Provider]
		base := pc.BaseURL
		if base == "" {
			base = "default"
		}
		general := [][2]string{
			{"Provider", st.Provider},
			{"Model", st.Model},
			{"API key", maskKey(pc.APIKey)},
			{"Base URL", base},
			{"File", transcript.ShortPath(config.UserSettingsPath())},
		}
		if root := a.Trust().Root; root != "" {
			general = append(general, [2]string{"Project file", transcript.ShortPath(config.ProjectSettingsPath(root))})
		}
		effort := st.Effort
		if effort == "" {
			effort = "unset"
		}
		runtime := [][2]string{
			{"Reasoning", effort},
			{"Context", transcript.Tokens(st.Window)},
			{"Max turns", fmt.Sprint(s.MaxTurns)},
			{"Mode", string(a.Mode())},
			{"Sub-agent model", st.SmallModel},
		}
		if s.CompactRatio > 0 {
			runtime = append(runtime, [2]string{"Compact at", fmt.Sprintf("%.0f%%", s.CompactRatio*100)})
		}
		var providers [][2]string
		for _, name := range slices.Sorted(maps.Keys(s.Providers)) {
			p := s.Providers[name]
			desc := fmt.Sprintf("%d %s", len(p.Models), plural(len(p.Models), "model"))
			if p.BaseURL != "" {
				desc += " · " + p.BaseURL
			}
			if name == st.Provider {
				name += " ✓"
			}
			providers = append(providers, [2]string{name, desc})
		}
		tab := func(name string, rows [][2]string) panel.Tab {
			return panel.Tab{Name: name, Body: func(w int) []string { return info(rows, w) }}
		}
		return show(&panel.Text{Title: "Settings", Tabs: []panel.Tab{tab("general", general), tab("runtime", runtime), tab("providers", providers)}})
	}}
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "••••"
	}
	return key[:4] + "…" + key[len(key)-4:]
}

func mcp(a *app.App) Command {
	return Command{Name: "mcp", Description: "Show the MCP servers", Run: func(string) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			servers := a.MCPStatus(ctx)
			if len(servers) == 0 {
				return transcript.Note("No MCP servers configured.")
			}
			lines := []string{mcpSummary(servers)}
			for _, s := range servers {
				lines = append(lines, "  "+s.Name+"  "+mcpState(s))
			}
			return transcript.Print(strings.Join(lines, "\n"))
		}
	}}
}

func mcpSummary(servers []app.MCPServer) string {
	if len(servers) == 0 {
		return "none"
	}
	var ok, failed, tools int
	for _, s := range servers {
		if s.Error != "" {
			failed++
			continue
		}
		ok++
		tools += s.ToolCount
	}
	return fmt.Sprintf("%d connected · %d failed · %d tools", ok, failed, tools)
}

func mcpState(s app.MCPServer) string {
	switch {
	case s.Error != "":
		return "failed: " + s.Error
	case s.ListError != "":
		return "listing tools failed: " + s.ListError
	}
	return fmt.Sprintf("%d tools", s.ToolCount)
}

// extensionRows lists the extensions in effect and where each comes from,
// then what of the folder waits for trust, what is replaced and what failed
// to load.
func extensionRows(a *app.App, servers []app.MCPServer) [][2]string {
	ext, t := a.Extensions(), a.Trust()
	folder := transcript.HomePath(t.Root)
	switch {
	case t.Root == "":
		folder = "none: the home directory"
	case len(t.Surface) == 0:
		folder += " · nothing to trust"
	case t.Trusted:
		folder += " · trusted"
	default:
		folder += " · not trusted · /trust"
	}
	rows := [][2]string{{"Folder", folder}, {"Skills", ""}}
	for _, s := range ext.Skills {
		rows = append(rows, [2]string{s.Name, s.Source})
	}
	if len(ext.Agents) > 0 {
		rows = append(rows, [2]string{"Agents", ""})
		for _, d := range ext.Agents {
			rows = append(rows, [2]string{d.Name, transcript.ShortPath(d.Origin)})
		}
	}
	if len(ext.MCP) > 0 {
		rows = append(rows, [2]string{"MCP servers", ""})
		for _, srv := range ext.MCP {
			state := "not connected"
			if i := slices.IndexFunc(servers, func(s app.MCPServer) bool { return s.Name == srv.Name }); i >= 0 {
				state = mcpState(servers[i])
			}
			from := string(srv.Scope)
			if srv.Plugin != "" {
				from = "plugin " + srv.Plugin
			}
			rows = append(rows, [2]string{srv.Name, from + " · " + state})
		}
	}
	if len(ext.Plugins) > 0 {
		rows = append(rows, [2]string{"Plugins", ""})
		for _, pl := range ext.Plugins {
			rows = append(rows, [2]string{pluginTitle(pl), string(pl.Scope) + " · " + pluginDetail(pl)})
		}
	}
	if len(ext.Hooks) > 0 {
		rows = append(rows, [2]string{"Hooks", ""})
		for _, h := range ext.Hooks {
			from := string(h.Scope)
			if h.Plugin != "" {
				from = "plugin " + h.Plugin
			}
			rows = append(rows, [2]string{from, h.Detail()})
		}
	}
	if t.Held() {
		rows = append(rows, [2]string{"Waiting for trust", ""})
		for _, it := range t.Surface {
			rows = append(rows, [2]string{it.Kind, it.Detail})
		}
	}
	if len(ext.Shadowed) > 0 {
		rows = append(rows, [2]string{"Replaced", ""})
		for _, s := range ext.Shadowed {
			rows = append(rows, [2]string{s.Kind + " " + s.Name, transcript.ShortPath(s.Lost) + " by " + transcript.ShortPath(s.Won)})
		}
	}
	if len(ext.Problems) > 0 {
		rows = append(rows, [2]string{"Problems", ""})
		for _, err := range ext.Problems {
			rows = append(rows, [2]string{"!", err.Error()})
		}
	}
	return rows
}

func reload(a *app.App) Command {
	return Command{Name: "reload", Description: "Reload skills, agents, plugins, hooks, MCP servers and permission rules", Idle: true, Run: func(string) tea.Cmd {
		return func() tea.Msg {
			r, err := a.Reload(context.Background())
			if err != nil {
				return transcript.Fail("Reload failed: " + err.Error())
			}
			return transcript.Note(fmt.Sprintf("Reloaded %d skills and %d plugins", r.Skills, r.Plugins) + connected(r))
		}
	}}
}

func memory(a *app.App) Command {
	return Command{Name: "memory", Args: "[edit]", Description: "Show or edit the project's memory", Run: func(arg string) tea.Cmd {
		cwd := a.Cwd()
		dir, index := config.MemoryDir(cwd), config.MemoryFilePath(cwd)
		if arg == "edit" {
			config.EnsureMemoryDir(cwd)
			if _, err := os.Stat(index); os.IsNotExist(err) {
				_ = os.WriteFile(index, []byte("# Project Memory\n"), 0o644)
			}
			conv := a.Current()
			return editor.Open(index, func(err error) tea.Msg {
				if err != nil {
					return transcript.Fail("Editor: " + err.Error())
				}
				conv.Reload()
				return transcript.Note("Memory reloaded")
			})
		}
		var files []string
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				files = append(files, e.Name())
			}
		}
		text := "Memory in " + transcript.ShortPath(dir)
		if len(files) == 0 {
			text += "\nNo memories yet; the agent writes them as it learns."
		} else {
			text += "\n  " + strings.Join(files, "\n  ")
		}
		return output(text + "\n/memory edit opens the index in your editor.")
	}}
}
