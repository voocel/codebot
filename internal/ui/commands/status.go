package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/cron"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/plan"
	"github.com/voocel/codebot/internal/plugin"
	"github.com/voocel/codebot/internal/ui/tui"
)

// StatusCommand drives /status — a tabbed modal overlay reporting the live
// runtime snapshot of the current session: who you are, what you're running,
// what you've spent, and what's plugged in.
//
// /status answers "what does the agent look like *right now*". /settings is
// the static config side of the same coin; /debug-harness is the internal
// observability side. The three should never duplicate each other.
type StatusCommand struct {
	Session   *agent.Session
	Overlay   OverlayController
	Approval  *approval.Engine
	Plugins   *plugin.Catalog
	MCP       *mcpclient.Manager
	Cron      *cron.Store
	PlanPhase func() plan.Phase

	Cwd       string
	GitBranch string
	Version   string

	// SkillCount / CommandCount are read on every render so /reload-driven
	// changes are reflected without re-registering the command.
	SkillCount   func() int
	CommandCount func() int

	state *statusState
}

type statusState struct {
	active int

	// Captured at Run() so View() stays cheap — every keypress (including tab
	// switches) calls View, and live MCP/session lookups would dominate.
	// Refresh by re-opening /status.
	mcpServers []mcpclient.ServerStatus
}

var statusTabs = []string{"overview", "session", "usage", "runtime"}

func (c *StatusCommand) Spec() Spec {
	return Spec{
		Name:        "status",
		Usage:       "/status",
		Description: "Show current session status",
		Category:    "info",
		Kind:        KindBuiltin,
	}
}

func (c *StatusCommand) Run(_ Invocation) tea.Cmd {
	st := &statusState{active: 0}
	if c.MCP != nil {
		// Status() fans out ListTools to every connected server in parallel.
		// We're synchronous here (Run blocks the BubbleTea main loop) so the
		// timeout has to be short — connected servers respond in tens of ms;
		// anything still pending after this budget shows up as "list failed".
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		st.mcpServers = c.MCP.Status(ctx)
	}
	c.state = st
	c.Overlay.SetOverlay(c)
	return nil
}

func (c *StatusCommand) Active() bool  { return c.state != nil }
func (c *StatusCommand) IsModal() bool { return true }
func (c *StatusCommand) Dismiss()      { c.state = nil }

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
		c.Overlay.ClearOverlay()
		return true, nil
	}
	return true, nil
}

func (c *StatusCommand) View(width, height int) string {
	if c.state == nil {
		return ""
	}
	subtitle := ""
	if c.Version != "" {
		subtitle = c.Version
	}
	frame := tui.InfoOverlayFrame{
		Title:    "Status",
		Subtitle: subtitle,
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

// newPanel returns a width-aware InfoPanel so long values wrap rather than
// overflow. The width passed in is the inner content width that
// InfoOverlayFrame already computed for the body.
func newPanel(width int) *tui.InfoPanel {
	p := tui.NewInfoPanel("")
	p.SetWidth(width)
	return p
}

// ---------- overview ----------

func (c *StatusCommand) renderOverview(width int) string {
	s := c.Session.Settings()
	usage := c.Session.ContextUsage()
	inTok, outTok, cost := c.Session.CostEstimate()

	p := newPanel(width)
	p.Row("Model", fmt.Sprintf("%s · %s", s.Provider, c.Session.ModelName()))
	p.Row("Mode", c.modeLabel())
	p.Row("Context", formatContextSummary(usage))
	p.Row("Cost", formatCostSummary(inTok, outTok, cost))
	p.Row("Cwd", tui.ShortenPath(c.Cwd))
	if c.GitBranch != "" {
		p.Row("Git", c.GitBranch)
	}
	p.Row("Messages", fmt.Sprintf("%d", len(c.Session.Messages())))
	if info, err := c.Session.CurrentSessionInfo(); err == nil && !info.Created.IsZero() {
		p.Row("Session age", formatAge(time.Since(info.Created)))
	}
	return p.Render()
}

// ---------- session ----------

func (c *StatusCommand) renderSession(width int) string {
	info, err := c.Session.CurrentSessionInfo()
	if err != nil {
		return tui.MutedStyle.Render("  No active session.")
	}

	name := info.Name
	if name == "" {
		name = tui.MutedStyle.Render("(auto)")
	}

	p := newPanel(width)
	p.Row("ID", info.ID)
	p.Row("Name", name)
	p.Hint("Path", tui.ShortenPath(info.Path))
	p.Row("Cwd", tui.ShortenPath(info.Cwd))
	p.Row("Created", info.Created.Format("2006-01-02 15:04:05"))
	if !info.Created.IsZero() {
		p.Hint("Age", formatAge(time.Since(info.Created)))
	}
	p.Row("Messages", fmt.Sprintf("%d", len(c.Session.Messages())))
	return p.Render()
}

// ---------- usage ----------

func (c *StatusCommand) renderUsage(width int) string {
	s := c.Session.Settings()
	usage := c.Session.ContextUsage()
	inTok, outTok, cost := c.Session.CostEstimate()

	p := newPanel(width)
	p.Row("Tokens in", tui.FormatTokens(inTok))
	p.Row("Tokens out", tui.FormatTokens(outTok))
	p.Row("Cost", fmt.Sprintf("~$%.4f", cost))

	if cs := c.Session.CacheStats(); cs.Input > 0 {
		p.Section("Cache")
		p.Row("Hit rate", fmt.Sprintf("%.1f%%   (%s of %s)",
			cs.HitRate*100,
			tui.FormatTokens(cs.ReadTokens),
			tui.FormatTokens(cs.Input)))
		p.Row("Read", tui.FormatTokens(cs.ReadTokens))
		p.Row("Written", tui.FormatTokens(cs.WriteTokens))
		if cs.SavedUSD > 0 {
			p.Row("Saved", fmt.Sprintf("~$%.4f", cs.SavedUSD))
		}
		p.Hint("Retention", "ephemeral (5m)")
	}

	p.Section("Context")
	p.Row("Used", formatContextSummary(usage))
	if usage != nil {
		p.Hint("Detail", fmt.Sprintf("usage=%s · trailing=%s",
			tui.FormatTokens(usage.UsageTokens), tui.FormatTokens(usage.TrailingTokens)))
	}
	if s.CompactRatio > 0 {
		p.Hint("Compact at", fmt.Sprintf("%.0f%%", s.CompactRatio*100))
	}

	if last, ok := c.Session.LastRunSummary(); ok {
		p.Section("Last turn")
		p.Row("Turns", fmt.Sprintf("%d", last.TurnCount))
		p.Row("Tool calls", fmt.Sprintf("%d", last.ToolCalls))
		if last.ToolErrors > 0 {
			p.Hint("Tool errors", fmt.Sprintf("%d", last.ToolErrors))
		}
		if last.EndReason != "" {
			p.Hint("End reason", string(last.EndReason))
		}
	}

	p.Blank()
	p.Hint("Note", "Run /context for per-category breakdown.")
	return p.Render()
}

// ---------- runtime ----------

func (c *StatusCommand) renderRuntime(width int) string {
	s := c.Session.Settings()

	p := newPanel(width)
	p.Row("Approval", c.modeLabel())
	if c.PlanPhase != nil {
		if phase := c.PlanPhase(); phase != "" && phase != plan.PhaseOff {
			p.Row("Plan phase", string(phase))
		}
	}
	effort := s.ReasoningEffort
	if effort == "" {
		effort = tui.MutedStyle.Render("(unset)")
	}
	p.Row("Reasoning Effort", effort)

	p.Section("Extensions")
	p.Row("MCP", c.formatMCPSummary())
	for _, line := range c.formatMCPDetail() {
		p.Hint("", line)
	}
	p.Row("Plugins", c.formatPluginsSummary())
	p.Row("Skills", fmt.Sprintf("%d", c.skillCount()))
	p.Row("Commands", fmt.Sprintf("%d", c.commandCount()))
	p.Row("Hooks", formatHooksSummary(s.Hooks))
	p.Row("Loop tasks", c.formatLoopSummary())

	return p.Render()
}

// ---------- helpers ----------

func (c *StatusCommand) modeLabel() string {
	if c.Approval == nil {
		return "(unset)"
	}
	if c.Approval.PlanMode() {
		return "plan"
	}
	return formatApprovalMode(c.Approval.Mode())
}

func (c *StatusCommand) skillCount() int {
	if c.SkillCount == nil {
		return 0
	}
	return c.SkillCount()
}

func (c *StatusCommand) commandCount() int {
	if c.CommandCount == nil {
		return 0
	}
	return c.CommandCount()
}

func (c *StatusCommand) formatMCPSummary() string {
	if c.MCP == nil || len(c.state.mcpServers) == 0 {
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

	servers := append([]mcpclient.ServerStatus(nil), c.state.mcpServers...)
	sort.SliceStable(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })

	out := make([]string, 0, len(servers))
	for _, s := range servers {
		switch {
		case s.Error != "":
			out = append(out, fmt.Sprintf("%s %s · %s", dotErr, s.Name, truncateStr(s.Error, 40)))
		case s.ListError != "":
			out = append(out, fmt.Sprintf("%s %s · list failed", dotErr, s.Name))
		default:
			out = append(out, fmt.Sprintf("%s %s · %d tools", dot, s.Name, s.ToolCount))
		}
	}
	return out
}

func (c *StatusCommand) formatPluginsSummary() string {
	if c.Plugins == nil {
		return tui.MutedStyle.Render("(none)")
	}
	loaded := c.Plugins.Plugins()
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

func (c *StatusCommand) formatLoopSummary() string {
	if c.Cron == nil {
		return tui.MutedStyle.Render("(none)")
	}
	n := c.Cron.Len()
	if n == 0 {
		return tui.MutedStyle.Render("(none)")
	}
	return fmt.Sprintf("%d active", n)
}

// formatContextSummary renders "12.3k / 200k (6.2%)" or "(unknown)".
func formatContextSummary(u *agentcore.ContextUsage) string {
	if u == nil {
		return tui.MutedStyle.Render("(unknown)")
	}
	if u.ContextWindow <= 0 {
		return tui.FormatTokens(u.Tokens)
	}
	return fmt.Sprintf("%s / %s (%.1f%%)",
		tui.FormatTokens(u.Tokens),
		tui.FormatTokens(u.ContextWindow),
		u.Percent)
}

func formatCostSummary(in, out int, cost float64) string {
	if in+out == 0 {
		return tui.MutedStyle.Render("(no usage yet)")
	}
	return fmt.Sprintf("~$%.4f  (%s in · %s out)",
		cost, tui.FormatTokens(in), tui.FormatTokens(out))
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
