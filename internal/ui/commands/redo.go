package commands

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/ui/tui"
)

// RedoCommand drives /redo — it re-applies the file changes undone by the last
// /undo, returning the workspace to the state just before that undo. A new turn
// (fresh edits) clears the redo branch.
type RedoCommand struct {
	session *agent.Session
}

// Redo constructs the /redo command.
func Redo(session *agent.Session) *RedoCommand {
	return &RedoCommand{session: session}
}

func (c *RedoCommand) Spec() Spec {
	return Spec{
		Name:        "redo",
		Usage:       "/redo",
		Description: "Redo the file changes undone by the last /undo",
		Category:    "session",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}
}

func (c *RedoCommand) Run(_ Invocation) tea.Cmd {
	if notice := snapshotUnavailable(c.session); notice != nil {
		return notice
	}
	changed, ok, err := c.session.Redo()
	switch {
	case err != nil:
		return tui.SendCommandResult(tui.ErrorStyle.Render("Redo failed: " + err.Error()))
	case !ok:
		return tui.SendCommandResult(tui.CommandStyle.Render(
			"Nothing to redo. /redo only reverses a /undo, and starting a new turn clears it."))
	case len(changed) == 0:
		return tui.SendCommandResult(tui.CommandStyle.Render("Nothing changed."))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Restored %d file(s) undone by /undo:", len(changed))
	for _, f := range changed {
		b.WriteString("\n  " + f)
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(b.String()))
}
