package commands

import (
	"context"
	"fmt"
	"maps"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/voocel/litellm/catalog"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/ui/tui/editor"
	"github.com/voocel/codebot/internal/ui/tui/panel"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

type modelChoice struct {
	provider, name string
	efforts        []string
	effort         int
}

// effortChip lights the chosen effort.
var effortChip = lipgloss.NewStyle().Background(theme.Surface).Foreground(theme.Accent).Bold(true)

// effortBar lays the efforts the model takes side by side, the chosen one lit.
func (c *modelChoice) effortBar() string {
	line := theme.MutedText.Render("Effort  ")
	if len(c.efforts) < 2 {
		return line + theme.SubtleText.Render("set by the provider for this model")
	}
	for i, e := range c.efforts {
		if e == "" {
			e = "auto"
		}
		st := theme.SubtleText
		if i == c.effort {
			st = effortChip
		}
		line += st.Render(" " + e + " ")
	}
	return line
}

// modelCells are the context window and the dollars per million input and
// output tokens, blank where the model list does not know them.
func modelCells(facts catalog.Model) []string {
	cells := make([]string, 3)
	if facts.MaxInputTokens > 0 {
		cells[0] = transcript.Tokens(facts.MaxInputTokens)
	}
	if p := facts.Pricing; p != nil {
		cells[1], cells[2] = perMillion(p.Input), perMillion(p.Output)
	}
	return cells
}

// perMillion shows whole dollars as such and cents to at least two places:
// $3, $0.10, $0.075.
func perMillion(rate float64) string {
	if rate == 0 {
		return "free"
	}
	s := strconv.FormatFloat(math.Round(rate*1e9)/1000, 'f', -1, 64)
	if i := strings.IndexByte(s, '.'); i >= 0 && len(s)-i == 2 {
		s += "0"
	}
	return "$" + s
}

// providerNote tells what a provider's name does not: the protocol it
// speaks and where it connects.
func providerNote(name string, pc config.ProviderConfig) string {
	var parts []string
	if pc.Type != "" && pc.Type != name {
		parts = append(parts, pc.Type)
	}
	if pc.API != "" {
		parts = append(parts, pc.API+" API")
	}
	if u, err := url.Parse(pc.BaseURL); err == nil && u.Host != "" {
		parts = append(parts, u.Host)
	}
	return strings.Join(parts, " · ")
}

func model(a *app.App) Command {
	return Command{Name: "model", Aliases: []string{"m"}, Description: "Switch the model and its reasoning effort", Idle: true, Run: func(string) tea.Cmd {
		st := a.Current().Status()
		providers := a.Settings().Providers
		notes := map[string]string{}
		var items []panel.Item
		for _, prov := range slices.Sorted(maps.Keys(providers)) {
			notes[prov] = providerNote(prov, providers[prov])
			for _, name := range providers[prov].Models {
				c := &modelChoice{provider: prov, name: name, efforts: a.ThinkingLevels(prov, name)}
				c.effort = max(slices.Index(c.efforts, st.Effort), 0)
				facts, _ := a.ModelFacts(prov, name)
				items = append(items, panel.Item{
					Group:   prov,
					Title:   name,
					Cells:   modelCells(facts),
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
			Header: []string{"context", "input", "output"},
			Notes:  notes,
			Footer: func(it *panel.Item) string { return it.Value.(*modelChoice).effortBar() },
			Filter: true,
			Hint:   theme.Hint("↑↓", "select", "←→", "effort", "enter", "switch", "esc", "close"),
			Keys: func(k string, it *panel.Item) (tea.Cmd, bool, bool) {
				if it == nil || (k != "left" && k != "right") {
					return nil, false, false
				}
				c := it.Value.(*modelChoice)
				step := 1
				if k == "left" {
					step = -1
				}
				c.effort = min(max(c.effort+step, 0), len(c.efforts)-1)
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

// mcp lists the servers. One that logs in with OAuth takes enter to log in,
// as when it asks for that, and x to log out.
func mcp(a *app.App) Command {
	return Command{Name: "mcp", Description: "See the MCP servers, log in or out", Run: func(string) tea.Cmd {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			servers := a.MCPStatus(ctx)
			if len(servers) == 0 {
				return transcript.Note("No MCP servers configured.")
			}
			var items []panel.Item
			for _, s := range servers {
				detail := mcpState(s)
				if s.Login {
					detail += " · enter"
				}
				items = append(items, panel.Item{Title: s.Name, Detail: detail, Value: s})
			}
			return &panel.List{
				Title: "MCP · " + mcpSummary(servers),
				Items: items,
				Hint:  theme.Hint("↑↓", "select", "enter", "log in", "x", "log out", "esc", "close"),
				Keys: func(k string, it *panel.Item) (tea.Cmd, bool, bool) {
					if it == nil || k != "enter" && k != "x" {
						return nil, false, false
					}
					s := it.Value.(app.MCPServer)
					switch {
					case !s.OAuth:
						return note(s.Name + " does not log in: it is no HTTP server, or it has an Authorization header."), true, false
					case k == "enter":
						return mcpLogin(a, s.Name), true, true
					}
					return mcpLogout(a, s.Name), true, true
				},
			}
		}
	}}
}

// loginTimeout bounds how long a login waits for the user in the browser.
const loginTimeout = 5 * time.Minute

func mcpLogin(a *app.App, name string) tea.Cmd {
	start := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		l, err := a.LoginMCP(ctx, name)
		if err != nil {
			return transcript.Fail("Could not log in to " + name + ": " + err.Error())
		}
		openBrowser(l.URL)
		wait := func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
			defer cancel()
			r, err := l.Wait(ctx)
			if err != nil {
				return transcript.Fail("Could not log in to " + name + ": " + err.Error())
			}
			return transcript.Note("Logged in to " + name + connected(r))
		}
		return tea.Sequence(note("Log in to "+name+" in the browser. If it did not open, visit:\n"+l.URL), wait)()
	}
	return tea.Sequence(note("Logging in to "+name+"…"), start)
}

func mcpLogout(a *app.App, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		r, err := a.LogoutMCP(ctx, name)
		if err != nil {
			return transcript.Fail("Could not log out of " + name + ": " + err.Error())
		}
		return transcript.Note("Logged out of " + name + connected(r))
	}
}

func mcpSummary(servers []app.MCPServer) string {
	if len(servers) == 0 {
		return "none"
	}
	var ok, login, failed, tools int
	for _, s := range servers {
		switch {
		case s.Login:
			login++
		case s.Error != "":
			failed++
		default:
			ok++
			tools += s.ToolCount
		}
	}
	summary := fmt.Sprintf("%d connected · %d failed · %d tools", ok, failed, tools)
	if login > 0 {
		summary += fmt.Sprintf(" · %d waiting for login", login)
	}
	return summary
}

func mcpState(s app.MCPServer) string {
	switch {
	case s.Login:
		return "needs login"
	case s.Error != "":
		return "failed: " + s.Error
	case s.ListError != "":
		return "listing tools failed: " + s.ListError
	}
	return fmt.Sprintf("%d tools", s.ToolCount)
}

func extensionRows(a *app.App, servers []app.MCPServer) [][2]string {
	ext, t := a.Extensions(), a.Trust()
	folder := transcript.HomePath(t.Root)
	switch {
	case t.Root == "":
		folder = "none: the home directory"
	case len(t.Surface) == 0:
		folder += " · nothing to trust"
	case t.ForRun:
		folder += " · trusted for this run by --trust"
	case t.Denied || len(t.Agreed) == 0:
		folder += " · not trusted · /trust"
	case len(t.Held()) == 0:
		folder += " · trusted"
	default:
		folder += fmt.Sprintf(" · trusted to %d of %d · /trust", len(t.Agreed), len(t.Surface))
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
				if servers[i].Login {
					state += " · /mcp"
				}
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
	if held := t.Held(); len(held) > 0 {
		rows = append(rows, [2]string{"Off until you trust them", ""})
		for _, it := range held {
			state := " · waiting for you"
			if t.Denied || t.Declined.Has(it) {
				state = " · declined"
			}
			rows = append(rows, [2]string{panel.KindLabel(it.Kind), it.Detail + state})
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
			return transcript.Note(fmt.Sprintf("Reloaded %d skills and %d plugins", r.Skills, r.Plugins) + connected(r.MCP))
		}
	}}
}

// memory lists what every request here starts from: the instructions of the
// project and the user's own, and the memory the agent keeps. Enter edits one.
func memory(a *app.App) Command {
	return Command{Name: "memory", Description: "Edit the instructions and memory every request starts from", Run: func(string) tea.Cmd {
		cwd := a.Cwd()
		instructions := func(dir string) string {
			path, claude := filepath.Join(dir, "AGENTS.md"), filepath.Join(dir, "CLAUDE.md")
			if !exists(path) && exists(claude) {
				return claude
			}
			return path
		}
		project, user := instructions(config.ProjectRoot(cwd)), instructions(config.UserConfigDir())
		items := []panel.Item{
			{Group: "Instructions", Title: filepath.Base(project), Detail: "this project · " + size(project, "/init drafts it"), Value: project},
			{Group: "Instructions", Title: transcript.HomePath(user), Detail: "every project · " + size(user, "none yet"), Value: user},
		}
		dir, index := config.MemoryDir(cwd), config.MemoryFilePath(cwd)
		items = append(items, panel.Item{Group: "Memory the agent keeps", Title: filepath.Base(index), Detail: "the index · " + size(index, "none yet"), Value: index})
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if path := filepath.Join(dir, e.Name()); !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && path != index {
				items = append(items, panel.Item{Group: "Memory the agent keeps", Title: e.Name(), Detail: size(path, ""), Value: path})
			}
		}
		conv := a.Current()
		return show(&panel.List{Title: "Memory", Items: items, Hint: theme.Hint("↑↓", "select", "enter", "edit", "esc", "close"), Select: func(it panel.Item) tea.Cmd {
			path := it.Value.(string)
			if path == index {
				config.EnsureMemoryDir(cwd) // a worktree's memory may have no directory yet
			}
			return editor.Open(path, func(err error) tea.Msg {
				if err != nil {
					return transcript.Fail("Editor: " + err.Error())
				}
				conv.Reload()
				return transcript.Note(it.Title + " saved: the next request reads it.")
			})
		}})
	}}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// size tells how long a file is, or none when it doesn't exist.
func size(path, none string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return none
	}
	n := strings.Count(strings.TrimRight(string(data), "\n"), "\n") + 1
	return fmt.Sprintf("%d %s", n, plural(n, "line"))
}
