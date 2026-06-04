package commands

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/snapshot"
	"github.com/voocel/codebot/internal/ui/tui"
)

// UndoCommand drives /undo — it reverts workspace files to the start of the
// most recent turn, undoing the agent's last round of edits. Conversation
// history is left untouched.
type UndoCommand struct {
	session *agent.Session
}

// Undo constructs the /undo command.
func Undo(session *agent.Session) *UndoCommand {
	return &UndoCommand{session: session}
}

func (c *UndoCommand) Spec() Spec {
	return Spec{
		Name:        "undo",
		Usage:       "/undo",
		Description: "Undo the last turn's file changes",
		Category:    "session",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}
}

func (c *UndoCommand) Run(_ Invocation) tea.Cmd {
	changed, ok, err := c.session.Undo()
	switch {
	case errors.Is(err, snapshot.ErrSnapshotExpired):
		return tui.SendCommandResult(tui.CommandStyle.Render(
			"The last checkpoint has expired (the shadow repo reclaimed it after 7 days) and can no longer be restored."))
	case err != nil:
		return tui.SendCommandResult(tui.ErrorStyle.Render("Undo failed: " + err.Error()))
	case !ok:
		return tui.SendCommandResult(tui.CommandStyle.Render(
			"Nothing to undo. File checkpoints are kept only inside a git repository for the current session."))
	case len(changed) == 0:
		return tui.SendCommandResult(tui.CommandStyle.Render("The last turn made no file changes."))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Reverted %d file(s) from the last turn:", len(changed))
	for _, f := range changed {
		b.WriteString("\n  " + f)
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(b.String()))
}
