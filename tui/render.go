package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
)

// RebuildViewport reconstructs the viewport content from blocks and streaming state.
func (m *Model) RebuildViewport() {
	var sb strings.Builder

	for _, b := range m.Blocks {
		if b.Collapsed {
			if b.Summary != "" {
				sb.WriteString(MutedStyle.Render("▸ " + b.Summary))
				sb.WriteString("\n")
			}
			continue
		}
		sb.WriteString(b.Content)
		sb.WriteString("\n\n")
	}

	if m.IsStream {
		sb.WriteString(AssistantPrefixStyle.Render("> Assistant"))
		sb.WriteString("\n")
		sb.WriteString(StreamingStyle.Render(m.Streaming.String()))
		sb.WriteString(m.Spinner.View())
		sb.WriteString("\n\n")
	}

	if len(m.PendingTools) > 0 && !m.IsStream {
		for _, name := range m.PendingTools {
			sb.WriteString(m.Spinner.View())
			sb.WriteString(" ")
			sb.WriteString(ToolNameStyle.Render(name))
			sb.WriteString(" running...\n")
		}
		sb.WriteString("\n")
	}

	m.Viewport.SetContent(sb.String())
	if m.AutoScroll {
		m.Viewport.GotoBottom()
	}
}

// RenderStatusBar renders the status bar with optional right-side customization.
func (m *Model) RenderStatusBar() string {
	var status string
	if m.Running {
		status = m.Spinner.View() + " Thinking..."
	} else {
		status = "Ready"
	}

	right := fmt.Sprintf("%s  Turn %d", m.ModelName, m.TurnCount)
	if m.config.StatusRight != nil {
		if extra := m.config.StatusRight(m); extra != "" {
			right = extra + "  " + right
		}
	}

	gap := max(m.Width-lipgloss.Width(status)-lipgloss.Width(right)-2, 1)

	bar := status + strings.Repeat(" ", gap) + right
	return StatusBarStyle.Width(m.Width).Render(bar)
}

// RenderFooter renders the optional footer bar above the input area.
func (m *Model) RenderFooter() string {
	if m.config.OnFooter == nil {
		return ""
	}
	content := m.config.OnFooter(m)
	if content == "" {
		return ""
	}
	return FooterStyle.Width(m.Width).Render(content)
}

// RenderMarkdown renders markdown content using glamour.
func (m *Model) RenderMarkdown(content string) string {
	if m.Glamour == nil || content == "" {
		return content
	}
	rendered, err := m.Glamour.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimSpace(rendered)
}

// --- Utility functions ---

// MsgRole safely extracts the role from an AgentMessage.
func MsgRole(msg agentcore.AgentMessage) agentcore.Role {
	if msg == nil {
		return ""
	}
	return msg.GetRole()
}

// FormatToolArgs formats tool arguments for display, truncating if needed.
func FormatToolArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	s := string(args)
	if len(s) > 100 {
		s = s[:97] + "..."
	}
	return s
}

// FormatToolResult formats a tool result for display.
func FormatToolResult(result json.RawMessage, isError bool) string {
	prefix := ""
	if isError {
		prefix = "error: "
	}
	if len(result) == 0 {
		return prefix + "(no output)"
	}
	s := strings.TrimSpace(string(result))
	lines := strings.SplitN(s, "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
		s = strings.Join(lines, "\n") + "\n..."
	}
	if len(s) > 300 {
		s = s[:297] + "..."
	}
	return prefix + s
}

// TruncateLines truncates text to maxLines, appending "..." if truncated.
func TruncateLines(s string, maxLines int) string {
	lines := strings.SplitN(s, "\n", maxLines+1)
	if len(lines) > maxLines {
		return strings.Join(lines[:maxLines], "\n") + "..."
	}
	return s
}

// RenderStreamingOutput shows the last N lines of streaming tool output.
func RenderStreamingOutput(full string, maxLines int) string {
	all := strings.TrimRight(full, "\n")
	lines := strings.Split(all, "\n")
	start := max(len(lines)-maxLines, 0)
	visible := lines[start:]
	var sb strings.Builder
	if start > 0 {
		sb.WriteString(MutedStyle.Render(fmt.Sprintf("    ... (%d lines above)", start)))
		sb.WriteByte('\n')
	}
	for _, line := range visible {
		sb.WriteString(MutedStyle.Render("    " + line))
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// RenderEditResult renders the edit tool result with colored diff output.
func RenderEditResult(result json.RawMessage) string {
	if len(result) == 0 {
		return ToolResultStyle.Render("    (edit completed)")
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		return ToolResultStyle.Render("    " + TruncateLines(string(result), 10))
	}

	msg, _ := parsed["message"].(string)
	diff, _ := parsed["diff"].(string)
	if diff == "" {
		return ToolResultStyle.Render("    " + msg)
	}

	var sb strings.Builder
	sb.WriteString(CommandStyle.Render("    "+msg) + "\n")
	for _, line := range strings.Split(diff, "\n") {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "-"):
			sb.WriteString(ErrorStyle.Render("    " + line))
		case strings.HasPrefix(line, "+"):
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("    " + line))
		default:
			sb.WriteString(MutedStyle.Render("    " + line))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// FormatProgressLine formats a tool progress update for display.
func FormatProgressLine(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	s := string(result)
	if len(s) > 200 {
		s = s[:197] + "..."
	}
	return s
}
