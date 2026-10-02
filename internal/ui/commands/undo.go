package commands

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui"
)

// UndoCommand drives /undo — it reverts workspace files to the start of the
// most recent turn, undoing the agent's last round of edits. Conversation
// history is left untouched.
type UndoCommand struct {
	app *app.App
}

// Undo constructs the /undo command.
func Undo(a *app.App) *UndoCommand {
	return &UndoCommand{app: a}
}

func (c *UndoCommand) Spec() Spec {
	return Spec{
		Name:        "undo",
		Usage:       "/undo",
		Description: "Undo the last turn's file changes",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}
}

func (c *UndoCommand) Run(_ Invocation) tea.Cmd {
	changed, ok, err := c.app.Current().Undo()
	switch {
	case err != nil:
		return tui.SendCommandResult(tui.ErrorStyle.Render("Undo failed: " + err.Error()))
	case !ok:
		return tui.SendCommandResult(tui.CommandStyle.Render(
			"Nothing to undo — no file changes have been recorded in this session yet."))
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
