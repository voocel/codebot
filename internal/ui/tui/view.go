package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View renders the live area pinned at the bottom of the terminal.
// Completed content lives in terminal scrollback (printed via tea.Println).
func (m *Model) View() string {
	if !m.Ready {
		return "\n  Initializing..."
	}

	var parts []string
	overlay, overlayReplacesInput := m.overlayView()
	appendInputArea := func() {
		parts = append(parts, m.renderInputPanel())
	}

	if m.ShowWelcome {
		parts = append(parts, m.renderWelcome())
	}

	if m.IsStream {
		if thinking := strings.TrimSpace(m.Thinking.String()); thinking != "" {
			indented := indentBlock(ThinkingBodyStyle.Render(m.wrapTextForIndent(thinking, 2)), 2)
			parts = append(parts, "", ThinkingBodyStyle.Render("● ")+strings.TrimPrefix(indented, "  "))
		}
		indented := m.renderMarkdownBlock(m.Streaming.String(), 2)
		parts = append(parts, "", AssistantIconStyle.Render("● ")+strings.TrimPrefix(indented, "  ")+m.Spinner.View())
	}

	for id, name := range m.PendingTools {
		line := m.ToolSpinner.View() + " " + ToolNameStyle.Render(name)
		if buf, ok := m.ToolOutputBuf[id]; ok && buf.Len() > 0 {
			output := RenderStreamingOutput(buf.String(), 8)
			line += "\n" + indentBlock(m.wrapTextForIndent(output, 2), 2)
		}
		if tbuf, ok := m.ToolThinkingBuf[id]; ok && tbuf.Len() > 0 {
			text := strings.TrimSpace(tbuf.String())
			if idx := strings.LastIndex(text, "\n"); idx >= 0 {
				text = text[idx+1:]
			}
			line += "\n" + indentBlock(ThinkingBodyStyle.Render("thinking "+truncateRunes(text, 71)), 4)
		}
		if dbuf, ok := m.ToolDeltaBuf[id]; ok && dbuf.Len() > 0 {
			text := strings.ReplaceAll(strings.TrimSpace(dbuf.String()), "\n", " ")
			line += "\n" + indentBlock(ReplyLabelStyle.Render("reply ")+truncateRunes(text, 74), 4)
		}
		parts = append(parts, "", line)
	}

	parts = append(parts, "")

	if m.ShowSummary {
		parts = append(parts, m.renderRunSummary(), "")
	}
	if m.Tasks != nil && m.Tasks.Total > 0 {
		parts = append(parts, m.renderTaskList(), "")
	}

	if overlay != "" && overlayReplacesInput {
		parts = append(parts, overlay)
	} else if overlay != "" {
		if statusBar := m.RenderStatusBar(); statusBar != "" {
			parts = append(parts, statusBar, "")
		}
		appendInputArea()
		parts = append(parts, overlay)
	} else if planBar := m.RenderPlanBar(); planBar != "" {
		parts = append(parts, planBar)
		parts = append(parts, m.RenderStatusBar())
	} else if m.AskUser != nil {
		parts = append(parts, renderAskUser(m.AskUser))
	} else if m.Permission != nil {
		parts = append(parts, renderPermission(m.Permission))
		parts = append(parts, m.RenderStatusBar())
	} else {
		if statusBar := m.RenderStatusBar(); statusBar != "" {
			parts = append(parts, statusBar, "")
		}
		if len(m.QueuedMsgs) > 0 {
			parts = append(parts, m.renderQueuedMsgs())
		}
		appendInputArea()
		if comp := m.renderCompletions(); comp != "" {
			parts = append(parts, comp)
		}
	}

	if !m.compActive && overlay == "" && m.AskUser == nil {
		parts = append(parts, m.RenderContextBar())
		parts = append(parts, "")
	}

	return strings.Join(parts, "\n")
}

// RenderPromptOutput renders a user message with optional welcome banner for scrollback.
func (m *Model) RenderPromptOutput(text string) string {
	userLine := "\n" + m.renderUserMessage(text)
	if m.ShowWelcome {
		return m.renderWelcome() + "\n" + userLine
	}
	return userLine
}

func (m *Model) shellInputActive() bool {
	return strings.HasPrefix(strings.TrimSpace(m.Input.Value()), "!")
}

// overlayView returns the rendered overlay content and whether it replaces the input area.
func (m *Model) overlayView() (string, bool) {
	if m.config.Overlay == nil {
		return "", false
	}
	ov := m.config.Overlay(m)
	if ov == nil {
		return "", false
	}
	return ov.View(m.Width), ov.ReplacesInput
}

// renderCompletions renders the completion menu.
func (m *Model) renderCompletions() string {
	if !m.compActive || len(m.compItems) == 0 {
		return ""
	}
	return m.renderCommandPalette()
}

// styledInputView renders the textarea with optional command highlighting.
// When cmdHighlight is set, the command text in the view is colorized.
func (m *Model) styledInputView() string {
	view := m.Input.View()
	if m.shellInputActive() {
		view = strings.Replace(view, "❯", ShellAccentStyle.Render("❯"), 1)
		view = strings.Replace(view, "!", ShellAccentStyle.Render("!"), 1)
		return view
	}
	if m.cmdHighlight == "" {
		return view
	}
	colored := lipgloss.NewStyle().Foreground(ColorPrimary).Render(m.cmdHighlight)
	return strings.Replace(view, m.cmdHighlight, colored, 1)
}
