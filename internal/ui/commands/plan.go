package commands

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/plan"
	"github.com/voocel/codebot/internal/ui/tui"
)

// PlanCommand drives /plan — entering plan mode or managing the active
// plan. The plan state machine (review modal, key handling, agentcore event
// hooks) stays in the ui package because it spans multiple TUI subsystems;
// this command only routes the slash-command surface into it via the
// callbacks below.
type PlanCommand struct {
	Phase  func() plan.Phase
	Enter  func() tea.Cmd
	Show   func() tea.Cmd
	Cancel func() tea.Cmd
	Open   func() tea.Cmd
}

func (c *PlanCommand) Spec() Spec {
	return Spec{
		Name:        "plan",
		Usage:       "/plan [open|cancel]",
		Description: "Enter plan mode or manage plans",
		Category:    "plan",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}
}

func (c *PlanCommand) Run(inv Invocation) tea.Cmd {
	if len(inv.Args) == 0 {
		if c.Phase() == plan.PhaseOff {
			return c.Enter()
		}
		return c.Show()
	}

	switch strings.ToLower(strings.TrimSpace(inv.Args[0])) {
	case "cancel":
		return c.Cancel()
	case "open":
		return c.Open()
	}

	if c.Phase() != plan.PhaseOff {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			"Already in plan mode. Use /plan open to inspect the plan, or /plan cancel to exit first."))
	}
	return c.Enter()
}
