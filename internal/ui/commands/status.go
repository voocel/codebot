package commands

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/agentcore"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/ui/tui"
)

// StatusCommand drives /status — a tabbed modal overlay reporting the
// current conversation: what it runs on, what it has spent, and what is
// plugged in. /settings is the static configuration side.
type StatusCommand struct {
	app     *app.App
	overlay OverlayController
	version string

	state *statusState
}

// statusState is captured at Run so View stays cheap: every keypress renders
// it. Reopen /status to refresh.
type statusState struct {
	active     int
	status     app.Status
	info       app.SessionInfo
	messages   int
	mcpServers []app.MCPServer
}

var statusTabs = []string{"overview", "session", "usage", "runtime"}

func (c *StatusCommand) Spec() Spec {
	return Spec{
		Name:        "status",
		Usage:       "/status",
		Description: "Show current session status",
		Kind:        KindBuiltin,
	}
}

func (c *StatusCommand) Run(_ Invocation) tea.Cmd {
	conv := c.app.Current()
	st := &statusState{status: conv.Status(), messages: len(conv.History())}
	if sessions, err := c.app.Sessions(); err == nil {
		for _, info := range sessions {
			if info.ID == conv.ID() {
				st.info = info
			}
		}
	}
	// Status asks every connected server for its tools; this runs on the
	// TUI's goroutine, so the budget is short and slow servers show as
	// "list failed".
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	st.mcpServers = c.app.MCPStatus(ctx)
	c.state = st
	c.overlay.SetOverlay(c)
	return nil
}

func (c *StatusCommand) Active() bool { return c.state != nil }
func (c *StatusCommand) Dismiss()     { c.state = nil }

func (c *StatusCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if c.state == nil {
		return false, nil
	}
	switch msg.String() {
	case "tab", "right", "l":
		c.state.active = (c.state.active + 1) % len(statusTabs)
		return true, nil
	case "shift+tab", "left", "h":
		c.state.active = (c.state.active - 1 + len(statusTabs)) % len(statusTabs)
		return true, nil
	case "1", "2", "3", "4":
		idx := int(msg.Runes[0] - '1')
		if idx < len(statusTabs) {
			c.state.active = idx
		}
		return true, nil
	case "esc", "ctrl+c", "q":
		c.overlay.ClearOverlay()
		return true, nil
	}
	return true, nil
}

func (c *StatusCommand) View(width, height int) string {
	if c.state == nil {
		return ""
	}
	frame := tui.InfoOverlayFrame{
		Title:    "Status",
		Subtitle: c.version,
		Tabs: []tui.InfoOverlayTab{
			{Name: statusTabs[0], Body: c.renderOverview},
			{Name: statusTabs[1], Body: c.renderSession},
			{Name: statusTabs[2], Body: c.renderUsage},
			{Name: statusTabs[3], Body: c.renderRuntime},
		},
		Active: c.state.active,
		Hint:   "Tab / ←→ switch · 1-4 jump · Esc close",
		Width:  width,
		Height: height,
	}
	return frame.Render()
}

// ---------- overview ----------

func (c *StatusCommand) renderOverview(width int) string {
	st := c.state.status
	p := tui.NewInfoPanel(width)
	p.Row("Model", fmt.Sprintf("%s · %s", st.Provider, st.Model))
	p.Row("Mode", string(st.Mode))
	p.Row("Context", formatContext(st.Context, st.Window))
	p.Row("Cost", formatCost(st.Usage))
	p.Row("Cwd", tui.ShortenPath(st.Cwd))
	if st.Worktree != "" {
		p.Hint("Worktree", tui.ShortenPath(st.Worktree))
	}
	if branch := c.app.Current().GitBranch(); branch != "" {
		p.Row("Git", branch)
	}
	p.Row("Messages", fmt.Sprintf("%d", c.state.messages))
	if !c.state.info.Created.IsZero() {
		p.Row("Session age", formatAge(time.Since(c.state.info.Created)))
	}
	return p.Render()
}

// ---------- session ----------

func (c *StatusCommand) renderSession(width int) string {
	info := c.state.info
	p := tui.NewInfoPanel(width)
	p.Row("ID", c.state.status.SessionID)
	if info.Path != "" {
		p.Hint("Path", tui.ShortenPath(info.Path))
		p.Row("Cwd", tui.ShortenPath(info.Cwd))
		p.Row("Created", info.Created.Format("2006-01-02 15:04:05"))
		p.Hint("Age", formatAge(time.Since(info.Created)))
	}
	p.Row("Messages", fmt.Sprintf("%d", c.state.messages))
	if c.state.status.Tasks > 0 {
		p.Row("Background", fmt.Sprintf("%d running", c.state.status.Tasks))
	}
	return p.Render()
}

// ---------- usage ----------

func (c *StatusCommand) renderUsage(width int) string {
	st := c.state.status
	u := st.Usage
	p := tui.NewInfoPanel(width)
	p.Row("Tokens in", tui.FormatTokens(u.Input))
	p.Row("Tokens out", tui.FormatTokens(u.Output))
	p.Row("Cost", formatCost(u))

	if u.CacheRead+u.CacheWrite > 0 {
		p.Section("Cache")
		if u.Input > 0 {
			p.Row("Hit rate", fmt.Sprintf("%.1f%%", float64(u.CacheRead)*100/float64(u.Input)))
		}
		p.Row("Read", tui.FormatTokens(u.CacheRead))
		p.Row("Written", tui.FormatTokens(u.CacheWrite))
	}

	p.Section("Context")
	p.Row("Used", formatContext(st.Context, st.Window))

	if last := st.LastRun; last != nil {
		p.Section("Last run")
		p.Row("Turns", fmt.Sprintf("%d", last.Turns))
		p.Row("Tool calls", fmt.Sprintf("%d", last.ToolCalls))
		if last.FailedCalls > 0 {
			p.Hint("Failed calls", fmt.Sprintf("%d", last.FailedCalls))
		}
		p.Hint("End reason", string(last.Reason))
	}
	return p.Render()
}

// ---------- runtime ----------

func (c *StatusCommand) renderRuntime(width int) string {
	st := c.state.status
	p := tui.NewInfoPanel(width)
	p.Row("Approval", string(st.Mode))
	effort := st.Effort
	if effort == "" {
		effort = tui.MutedStyle.Render("(provider default)")
	}
	p.Row("Reasoning Effort", effort)

	p.Section("Extensions")
	p.Row("MCP", c.formatMCPSummary())
	for _, line := range c.formatMCPDetail() {
		p.Hint("", line)
	}
	p.Row("Plugins", formatPluginsSummary(c.app.Plugins()))
	p.Row("Skills", fmt.Sprintf("%d", len(c.app.Current().Skills())))
	p.Row("Hooks", formatHooksSummary(c.app.Settings().Hooks))
	return p.Render()
}

// ---------- helpers ----------

func (c *StatusCommand) formatMCPSummary() string {
	if len(c.state.mcpServers) == 0 {
		return tui.MutedStyle.Render("(none)")
	}
	connected, failed, tools := 0, 0, 0
	for _, s := range c.state.mcpServers {
		if s.Error != "" {
			failed++
			continue
		}
		connected++
		tools += s.ToolCount
	}
	parts := []string{fmt.Sprintf("%d connected", connected)}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	parts = append(parts, fmt.Sprintf("%d tools", tools))
	return strings.Join(parts, " · ")
}

func (c *StatusCommand) formatMCPDetail() []string {
	if len(c.state.mcpServers) == 0 {
		return nil
	}
	dot := lipgloss.NewStyle().Foreground(tui.Success).Render("●")
	dotErr := lipgloss.NewStyle().Foreground(tui.Danger).Render("●")

	servers := slices.Clone(c.state.mcpServers)
	sort.SliceStable(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })

	out := make([]string, 0, len(servers))
	for _, s := range servers {
		switch {
		case s.Error != "":
			out = append(out, fmt.Sprintf("%s %s · %s", dotErr, s.Name, ansi.Truncate(s.Error, 40, "…")))
		case s.ListError != "":
			out = append(out, fmt.Sprintf("%s %s · list failed", dotErr, s.Name))
		default:
			out = append(out, fmt.Sprintf("%s %s · %d tools", dot, s.Name, s.ToolCount))
		}
	}
	return out
}

func formatPluginsSummary(loaded []app.Plugin) string {
	if len(loaded) == 0 {
		return tui.MutedStyle.Render("(none)")
	}
	enabled := 0
	for _, p := range loaded {
		if p.State.Enabled {
			enabled++
		}
	}
	return fmt.Sprintf("%d enabled / %d total", enabled, len(loaded))
}

func formatHooksSummary(hooks config.HooksConfig) string {
	if len(hooks) == 0 {
		return tui.MutedStyle.Render("(none)")
	}
	events := make([]string, 0, len(hooks))
	for ev := range hooks {
		events = append(events, ev)
	}
	sort.Strings(events)
	return strings.Join(events, " · ")
}

// formatContext renders "12.3k / 200k (6.2%)".
func formatContext(used, window int) string {
	if window <= 0 {
		return tui.FormatTokens(used)
	}
	return fmt.Sprintf("%s / %s (%.1f%%)", tui.FormatTokens(used), tui.FormatTokens(window), float64(used)*100/float64(window))
}

func formatCost(u agentcore.Usage) string {
	if u.Input+u.Output == 0 {
		return tui.MutedStyle.Render("(no usage yet)")
	}
	cost := 0.0
	if u.Cost != nil {
		cost = u.Cost.Total
	}
	return fmt.Sprintf("~$%.4f  (%s in · %s out)", cost, tui.FormatTokens(u.Input), tui.FormatTokens(u.Output))
}

// formatAge formats a duration as "Nm", "Nh Nm", or "Nd Nh".
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		days := int(d / (24 * time.Hour))
		h := int(d.Hours()) % 24
		return fmt.Sprintf("%dd %dh", days, h)
	}
}
