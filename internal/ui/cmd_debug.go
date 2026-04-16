package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/ui/tui"
)

// DebugHarnessCommand implements InteractiveCommand for /debug-harness.
// Opens a modal overlay with tabbed sections (turn / activity / metrics / context).
type DebugHarnessCommand struct {
	app   *App
	state *debugState
}

type debugState struct {
	active int

	metrics        agent.RuntimeMetricsSnapshot
	lastTurn       agent.TurnOutcomeSnapshot
	lastRunSummary agentcore.RunSummary
	hasRunSummary  bool
	recentTools    []agent.ToolCallSnapshot
	recentErrors   []agent.ErrorSnapshot
	lastReminder   agent.ReminderSnapshot
	hasReminder    bool
	lastCompaction agent.CompactionSnapshot
	hasCompaction  bool
	contextUsage   *agentcore.ContextUsage
}

var debugTabs = []string{"turn", "activity", "metrics", "context"}

func NewDebugHarnessCommand(app *App) *DebugHarnessCommand {
	return &DebugHarnessCommand{app: app}
}

func (c *DebugHarnessCommand) Spec() CommandSpec {
	return CommandSpec{
		Name:        "debug-harness",
		Usage:       "/debug-harness",
		Description: "Show harness runtime diagnostics",
		Category:    "info",
		Kind:        CommandKindBuiltin,
	}
}

func (c *DebugHarnessCommand) Run(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
	lastRunSummary, hasRunSummary := ctx.App.Session.LastRunSummary()
	lastReminder, hasReminder := ctx.App.Session.LastReminder()
	lastCompaction, hasCompaction := ctx.App.Session.LastCompaction()
	c.state = &debugState{
		active:         0,
		metrics:        ctx.App.Session.RuntimeMetrics(),
		lastTurn:       ctx.App.Session.LastTurnOutcome(),
		lastRunSummary: lastRunSummary,
		hasRunSummary:  hasRunSummary,
		recentTools:    ctx.App.Session.RecentToolCalls(5),
		recentErrors:   ctx.App.Session.RecentErrors(5),
		lastReminder:   lastReminder,
		hasReminder:    hasReminder,
		lastCompaction: lastCompaction,
		hasCompaction:  hasCompaction,
		contextUsage:   ctx.App.Session.ContextUsage(),
	}
	ctx.App.registry.SetOverlay(c)
	return nil
}

func (c *DebugHarnessCommand) Active() bool  { return c.state != nil }
func (c *DebugHarnessCommand) IsModal() bool { return true }
func (c *DebugHarnessCommand) Dismiss()      { c.state = nil }

func (c *DebugHarnessCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if c.state == nil {
		return false, nil
	}
	switch msg.String() {
	case "tab", "right", "l":
		c.state.active = (c.state.active + 1) % len(debugTabs)
		return true, nil
	case "shift+tab", "left", "h":
		c.state.active = (c.state.active - 1 + len(debugTabs)) % len(debugTabs)
		return true, nil
	case "1", "2", "3", "4":
		idx := int(msg.Runes[0] - '1')
		if idx < len(debugTabs) {
			c.state.active = idx
		}
		return true, nil
	case "esc", "ctrl+c", "q":
		c.app.registry.ClearOverlay()
		return true, nil
	}
	return true, nil
}

func (c *DebugHarnessCommand) View(width int) string {
	if c.state == nil {
		return ""
	}
	frame := tui.InfoOverlayFrame{
		Title: "Harness Debug",
		Tabs: []tui.InfoOverlayTab{
			{Name: debugTabs[0], Body: c.renderTurn},
			{Name: debugTabs[1], Body: c.renderActivity},
			{Name: debugTabs[2], Body: c.renderMetrics},
			{Name: debugTabs[3], Body: c.renderContext},
		},
		Active: c.state.active,
		Hint:   "Tab / ←→ switch · 1-4 jump · Esc close",
		Width:  width,
	}
	return frame.Render()
}

func (c *DebugHarnessCommand) renderTurn() string {
	s := c.state
	toolCalls := 0
	if s.hasRunSummary {
		toolCalls = s.lastRunSummary.ToolCalls
	}

	p := tui.NewInfoPanel("")
	p.Row("Assistant responded", formatBool(s.lastTurn.AssistantResponded))
	p.Row("Tool calls", fmt.Sprintf("%d", toolCalls))
	p.Row("Read-only tools", fmt.Sprintf("%d", s.lastTurn.ReadOnlyToolCalls))
	p.Row("Write-like tools", fmt.Sprintf("%d", s.lastTurn.WriteLikeToolCalls))
	p.Row("Task mutations", fmt.Sprintf("%d", s.lastTurn.TaskMutations))
	p.Hint("Run summary", formatRunSummary(s.lastRunSummary, s.hasRunSummary))
	p.Blank()
	p.Hint("Note", "Only the most recent completed agent run is reflected here.")
	return p.Render()
}

func (c *DebugHarnessCommand) renderActivity() string {
	s := c.state
	p := tui.NewInfoPanel("")

	tools := formatRecentToolCalls(s.recentTools)
	if len(tools) == 0 {
		p.Hint("Recent tool calls", "(none)")
	} else {
		for i, line := range tools {
			label := ""
			if i == 0 {
				label = "Recent tool calls"
			}
			p.Row(label, line)
		}
	}

	errors := formatRecentErrors(s.recentErrors)
	if len(errors) == 0 {
		p.Hint("Recent errors", "(none)")
	} else {
		for i, line := range errors {
			label := ""
			if i == 0 {
				label = "Recent errors"
			}
			p.Row(label, line)
		}
	}

	p.Hint("Last reminder", formatLastReminder(s.lastReminder, s.hasReminder))
	p.Hint("Last compaction", formatLastCompaction(s.lastCompaction, s.hasCompaction))
	return p.Render()
}

func (c *DebugHarnessCommand) renderMetrics() string {
	s := c.state
	p := tui.NewInfoPanel("")

	p.Row("Reminders total", fmt.Sprintf("%d", s.metrics.ReminderTotal))
	p.Hint("Reminder kinds", formatReminderCounts(s.metrics.ReminderByKind))

	p.Section("Compaction")
	p.Row("Total", fmt.Sprintf("%d", s.metrics.CompactionTotal))
	p.Row("Changed", fmt.Sprintf("%d", s.metrics.CompactionChanged))
	p.Row("Saved", tui.FormatTokens(s.metrics.CompactionSaved))
	p.Hint("By kind", formatCompactionCounts(s.metrics.CompactionByKind))
	p.Hint("Savings", formatCompactionSavings(s.metrics.CompactionSavedByKind))

	p.Section("Errors")
	p.Row("Total", fmt.Sprintf("%d", s.metrics.ErrorTotal))
	p.Hint("By kind", formatErrorCounts(s.metrics.ErrorByKind))

	p.Blank()
	p.Hint("Note", "Metrics accumulate since this session was created or loaded in the current process.")
	return p.Render()
}

func (c *DebugHarnessCommand) renderContext() string {
	s := c.state
	if s.contextUsage == nil {
		return tui.MutedStyle.Render("  Context usage unavailable.")
	}
	p := tui.NewInfoPanel("")
	p.Row("Context used", fmt.Sprintf("%s (%.1f%%)",
		tui.FormatTokens(s.contextUsage.Tokens), s.contextUsage.Percent))
	p.Row("Context window", tui.FormatTokens(s.contextUsage.ContextWindow))
	p.Hint("Detail", fmt.Sprintf("usage=%s, trailing=%s",
		tui.FormatTokens(s.contextUsage.UsageTokens), tui.FormatTokens(s.contextUsage.TrailingTokens)))
	return p.Render()
}
