package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
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
