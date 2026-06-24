package commands

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/ui/tui"
)

// WorktreeCommand drives /worktree — an isolated git-worktree sandbox the
// session works in, reviewed and merged or discarded on exit.
//
//	/worktree <name>   create a sandbox and move the session into it
//	/worktree exit     return to the main workspace, keeping changes for review
//	/worktree discard  return to the main workspace, discarding the sandbox
//
// Enter/Exit are wired to the runtime and return a ready-to-display message.
// They are nil outside the interactive TUI, where the command is unavailable.
type WorktreeCommand struct {
	Enter  func(name string) (string, error)
	Exit   func(discard bool) (string, error)
	Active func() bool
}

func (c *WorktreeCommand) Spec() Spec {
	return Spec{
		Name:        "worktree",
		Usage:       "/worktree <name> | exit | discard",
		Description: "Work in an isolated git worktree sandbox",
		Category:    "session",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}
}

func (c *WorktreeCommand) Run(inv Invocation) tea.Cmd {
	if c.Enter == nil || c.Exit == nil || c.Active == nil {
		return tui.SendCommandResult(tui.CommandStyle.Render("Worktree is only available in interactive mode."))
	}

	var (
		msg string
		err error
	)
	switch strings.TrimSpace(inv.RawArgs) {
	case "exit":
		msg, err = c.Exit(false)
	case "discard":
		msg, err = c.Exit(true)
	default:
		if c.Active() {
			return tui.SendCommandResult(tui.ErrorStyle.Render(
				"Already in a worktree — use /worktree exit or /worktree discard first."))
		}
		msg, err = c.Enter(strings.TrimSpace(inv.RawArgs))
	}
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Worktree: " + err.Error()))
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(msg))
}
