package tui

import (
	"encoding/json"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/todo"
)

// handleRestore replays the open conversation's history into scrollback,
// rendered as the live events render it, in a single block so it keeps its
// order.
func (m *Model) handleRestore() (tea.Model, tea.Cmd) {
	msgs := m.restored
	m.restored = nil
	if len(msgs) == 0 {
		return m, nil
	}
	m.ShowWelcome = false

	var sb strings.Builder
	sb.WriteString(m.renderWelcome())
	sb.WriteString("\n\n")
	sb.WriteString(MutedStyle.Render("  ── restored session ──"))

	calls := make(map[string]litellm.ToolUseBlock)
	answered := make(map[string]bool)
	for _, msg := range msgs {
		for _, tc := range msg.ToolCalls() {
			calls[tc.ID] = tc
		}
		if r, ok := msg.ToolResult(); ok {
			answered[r.ToolUseID] = true
		}
	}

	for _, msg := range msgs {
		switch msg.Role {
		case litellm.RoleUser:
			if text := restoredUserText(msg); text != "" {
				sb.WriteString("\n\n")
				sb.WriteString(m.renderUserMessage(text))
			}
		case litellm.RoleAssistant:
			if reply := m.renderAssistantMessage(msg); reply != "" {
				sb.WriteString("\n\n")
				sb.WriteString(reply)
			}
			// A call is shown with its result; one left without a result
			// shows its header alone.
			for _, tc := range msg.ToolCalls() {
				if !answered[tc.ID] && !app.HiddenToolCall(tc.Name, json.RawMessage(tc.Arguments)) {
					sb.WriteString("\n")
					sb.WriteString(toolCallHeader(tc.Name, json.RawMessage(tc.Arguments)))
				}
			}
		case litellm.RoleTool:
			r, _ := msg.ToolResult()
			tc := calls[r.ToolUseID]
			if app.HiddenToolCall(tc.Name, json.RawMessage(tc.Arguments)) {
				continue
			}
			sb.WriteString("\n")
			sb.WriteString(m.renderToolCall(toolCallHeader(tc.Name, json.RawMessage(tc.Arguments)), tc.Name, json.RawMessage(tc.Arguments), msg.Text(), r.IsError))
		}
	}

	sb.WriteString("\n\n")
	sb.WriteString(MutedStyle.Render("  ── end of history ──"))

	return m, tea.Batch(m.Emit(sb.String()), m.setTodos(todo.FromHistory(msgs)))
}

// restoredUserText is what the user wrote in msg, "" for a message the
// harness added. Reminders come before the user's text, in blocks of their
// own.
func restoredUserText(msg agentcore.Message) string {
	if msg.Kind != "" {
		return ""
	}
	var last string
	for _, block := range msg.Blocks {
		if t, ok := block.(litellm.TextBlock); ok && t.Text != "" {
			last = t.Text
		}
	}
	return last
}
