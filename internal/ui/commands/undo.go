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
	if notice := snapshotUnavailable(c.session); notice != nil {
		return notice
	}
	changed, ok, err := c.session.Undo()
	switch {
	case errors.Is(err, snapshot.ErrSnapshotExpired):
		return tui.SendCommandResult(tui.CommandStyle.Render(
			"The last checkpoint has expired (the shadow repo reclaimed it after 7 days) and can no longer be restored."))
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

// snapshotUnavailable returns an explanatory notice when workspace snapshots are
// off — the directory isn't a git repository, so /undo, /redo and /diff have
// nothing to operate on. Returns nil when snapshots are active. Shared by all
// three commands so they say *why* they're inert instead of pretending there is
// simply nothing to do.
func snapshotUnavailable(session *agent.Session) tea.Cmd {
	if session.SnapshotEnabled() {
		return nil
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(
		"Snapshots are off because this directory isn't a git repository — /undo, /redo and /diff are unavailable. Run `git init` here to enable them."))
}
