package commands

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/plan"
	"github.com/voocel/codebot/internal/ui/tui"
)

// PlanDeps groups the host callbacks that drive the /plan command. The plan
// state machine (review modal, key handling, agentcore event hooks) stays
// in the ui package because it spans multiple TUI subsystems; this command
// only routes the slash-command surface into it.
type PlanDeps struct {
	Phase  func() plan.Phase
	Enter  func(task string) tea.Cmd
	Show   func() tea.Cmd
	Cancel func() tea.Cmd
	Open   func() tea.Cmd
}

// Plan constructs the /plan command which enters plan mode or manages the
// active plan. With no args it enters plan mode (or shows the plan when
// already active); subcommands cancel/open manage the live plan.
func Plan(deps PlanDeps) Command {
	return NewSimple(Spec{
		Name:        "plan",
		Usage:       "/plan [open|cancel|<task>]",
		Description: "Enter plan mode or manage plans",
		Category:    "plan",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}, func(inv Invocation) tea.Cmd {
		if len(inv.Args) == 0 {
			if deps.Phase() == plan.PhaseOff {
				return deps.Enter("")
			}
			return deps.Show()
		}

		switch strings.ToLower(strings.TrimSpace(inv.Args[0])) {
		case "cancel":
			return deps.Cancel()
		case "open":
			return deps.Open()
		}

		if deps.Phase() != plan.PhaseOff {
			return tui.SendCommandResult(tui.ErrorStyle.Render(
				"Already in plan mode. Use /plan open to inspect the plan, or /plan cancel to exit first."))
		}
		return deps.Enter(strings.Join(inv.Args, " "))
	})
}
