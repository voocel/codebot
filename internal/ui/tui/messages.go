package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/storage"
)

// AgentEventMsg bridges agentcore events into the bubbletea Elm loop.
type AgentEventMsg struct {
	Event agentcore.Event
}

// CommandResultMsg carries the result of a slash command back to the model.
type CommandResultMsg struct {
	Text        string
	Quit        bool   // true for /exit
	Clear       bool   // true for /clear
	NewProvider      string // non-empty if provider was switched
	NewModel         string // non-empty if model was switched
	NewContextWindow int    // non-zero if context window changed
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
	Snapshot storage.TaskSnapshot
}

// HideCompletedTasksMsg hides the task card after all tasks stayed completed
// for a short delay. Version prevents stale timers from hiding a newer list.
type HideCompletedTasksMsg struct {
	Version uint64
}

// TasksRefreshMsg is a periodic tick that triggers a re-render of the /tasks overlay.
type TasksRefreshMsg struct{}

// MCPReadyMsg notifies the TUI that background MCP server connection has completed.
type MCPReadyMsg struct {
	Tools        int      // total number of tools loaded across all servers
	Errors       []string // connection errors (server: reason)
	Instructions string   // MCP server instructions to set as system suffix
}

// SuggestionMsg carries a prompt suggestion generated after agent completion.
type SuggestionMsg struct {
	Text string
}

// BtwResultMsg carries the result of a /btw side question back to the overlay.
type BtwResultMsg struct {
	Answer string
	Err    error
}
