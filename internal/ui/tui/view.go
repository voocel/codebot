package tui

// Model.View — composes the live bottom-pinned area from render_*.go helpers.

import (
	"strings"
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
			parts = append(parts, "", ThinkingIconStyle.Render("● ")+strings.TrimPrefix(indented, "  "))
		}
		indented := m.renderMarkdownBlock(m.Streaming.String(), 2)
		parts = append(parts, "", AssistantIconStyle.Render("● ")+strings.TrimPrefix(indented, "  ")+m.Spinner.View())
	}

	for id, name := range m.PendingTools {
		line := m.ToolSpinner.View() + " " + ToolNameStyle.Render(name)
		if buf, ok := m.ToolOutputBuf[id]; ok && buf.Len() > 0 {
			output := RenderStreamingOutput(buf.String(), ToolStreamTailLines)
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

	if m.RetryStatus != "" {
		parts = append(parts, "", MutedStyle.Render(m.RetryStatus))
	}

	parts = append(parts, "")

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
