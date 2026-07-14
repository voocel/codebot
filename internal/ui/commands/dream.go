package commands

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/dream"
	"github.com/voocel/codebot/internal/ui/tui"
)

// Dream constructs the /dream command: trigger a memory consolidation now,
// skipping the auto-trigger's time and session gates. The run happens in the
// background; /tasks shows progress and can kill it.
func Dream(d *dream.Dreamer) Command {
	return NewSimple(Spec{
		Name: "dream", Usage: "/dream", Description: "Consolidate auto memory now (runs in background)",
		Category: "session", Kind: KindBuiltin,
	}, func(inv Invocation) tea.Cmd {
		if d == nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("dream is not available in this mode"))
		}
		taskID, err := d.StartManual()
		if err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("dream: " + err.Error()))
		}
		return tui.SendCommandResult(tui.CommandStyle.Render(
			"Memory consolidation started (" + taskID + "). Watch progress with /tasks."))
	})
}
