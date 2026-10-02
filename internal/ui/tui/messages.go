package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/interact"
)

// AgentEventMsg bridges agentcore events into the bubbletea Elm loop.
type AgentEventMsg struct {
	Event agentcore.Event
}

// CommandResultMsg carries the result of a slash command back to the model.
type CommandResultMsg struct {
	Text string
	// Inline prints the result flush against the previous scrollback block
	// (no leading blank line). Use for output that should feel like a direct
	// continuation — e.g. shell command output under its echoed prompt.
	Inline bool
	Quit   bool // true for /exit
}

// SendCommandResult is a helper that wraps text into a CommandResultMsg tea.Cmd.
func SendCommandResult(text string) tea.Cmd {
	return func() tea.Msg { return CommandResultMsg{Text: text} }
}

// ImageAttachedMsg notifies the Model that an image has been pasted from clipboard.
type ImageAttachedMsg struct {
	Block litellm.Block // pre-built ImageBlock
}

// PasteTextMsg signals that Ctrl+V found no image; the textarea should paste text.
type PasteTextMsg struct{}

// PasteErrorMsg carries an error from clipboard paste or file drag-drop.
// Decrements Pasting counter and displays the error text.
type PasteErrorMsg struct {
	Text string
}

// hideCompletedTodosMsg hides the todo list after every item stayed completed
// for a short delay. Version prevents stale timers from hiding a newer list.
type hideCompletedTodosMsg struct {
	Version uint64
}

// TasksRefreshMsg is a periodic tick that triggers a re-render of the /tasks overlay.
type TasksRefreshMsg struct{}

// MCPReadyMsg notifies the TUI that background MCP server connection has completed.
type MCPReadyMsg struct {
	Tools  int      // total number of tools loaded across all servers
	Errors []string // connection errors (server: reason)
}

// SuggestionMsg carries a prompt suggestion generated after agent completion.
type SuggestionMsg struct {
	Text string
}

// statusTickMsg refreshes the status countdown.
type statusTickMsg struct{}

// ApplyMsg runs Apply on the TUI's goroutine: a command folds the result of
// its background work into its state with it, and may return a follow-up.
type ApplyMsg struct {
	Apply func() tea.Cmd
}

// OpenedMsg says Conversation replaced the open one.
type OpenedMsg struct {
	Conversation *app.Conversation
}

// ModeMsg says the permission mode changed.
type ModeMsg struct {
	Mode interact.Mode
}

// StatusChangedMsg carries the conversation's new status.
type StatusChangedMsg struct {
	Status app.Status
}

// RunStartedMsg says the conversation started a run.
type RunStartedMsg struct{}

// IdleMsg says the conversation has nothing left to run.
type IdleMsg struct{}
