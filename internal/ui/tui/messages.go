package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/tools"
)

// AgentEventMsg bridges agentcore events into the bubbletea Elm loop.
type AgentEventMsg struct {
	Event agentcore.Event
}

// CommandResultMsg carries the result of a slash command back to the model.
type CommandResultMsg struct {
	Text     string
	Quit     bool   // true for /exit
	Clear    bool   // true for /clear
	NewModel string // non-empty if model was switched
}

// SendCommandResult is a helper that wraps text into a CommandResultMsg tea.Cmd.
func SendCommandResult(text string) tea.Cmd {
	return func() tea.Msg { return CommandResultMsg{Text: text} }
}

// PromptMsg injects a message as if the user typed and sent it.
// The TUI renders it as a user message and forwards it to the agent.
type PromptMsg struct {
	Text string
}

// ImageAttachedMsg notifies the Model that an image has been pasted from clipboard.
type ImageAttachedMsg struct {
	Block agentcore.ContentBlock // pre-built ImageBlock (base64 + mime)
}

// PasteTextMsg signals that Ctrl+V found no image; the textarea should paste text.
type PasteTextMsg struct{}

// PasteErrorMsg carries an error from clipboard paste or file drag-drop.
// Decrements Pasting counter and displays the error text.
type PasteErrorMsg struct {
	Text string
}

// TaskListUpdateMsg notifies the TUI that the task list has changed.
type TaskListUpdateMsg struct {
	Snapshot tools.TaskSnapshot
}

// TasksRefreshMsg is a periodic tick that triggers a re-render of the /tasks overlay.
type TasksRefreshMsg struct{}
