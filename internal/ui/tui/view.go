package tui

// Model.View — composes the live bottom-pinned area from render_*.go helpers.

import (
	"fmt"
	"strings"
	"time"
)

// View renders the live area pinned at the bottom of the terminal.
// Completed content lives in terminal scrollback (printed via tea.Println).
func (m *Model) View() string {
	if !m.Ready {
		return "\n  Initializing..."
	}

	// Transcript modal takes over the entire viewport when open. We do not
	// emit any scrollback content (status bar, streaming bullets, input
	// panel) so the modal renders against an effectively blank canvas —
	// terminal scrollback above it stays untouched and reappears when the
	// modal closes. Skipped while the fleet list holds focus: that path keeps
	// the list pinned below the preview (renderFleetSplit), so the user can
	// switch agents without closing anything.
	if !m.FleetFocus {
		if body := m.transcriptViewBody(); body != "" {
			return body
		}
	}

	// Fleet split-preview: an agent is selected in the focused list, so show
	// its live transcript on top with the list pinned below.
	if m.FleetFocus && m.TranscriptModal != nil {
		return m.renderFleetSplit()
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
		// Only show the assistant bullet when there's actual streamed text.
		// An "empty bullet" frame appears when the assistant message contains
		// only tool_use blocks (e.g. hidden task_* calls) — IsStream goes
		// true at MessageStart, no text deltas arrive, then IsStream clears
		// at MessageEnd. The Running spinner in the status bar already
		// signals "agent is working", so we drop the bare bullet to avoid
		// the flash.
		if streamed := m.Streaming.String(); strings.TrimSpace(streamed) != "" {
			indented := m.RenderMarkdownBlock(streamed, 2)
			parts = append(parts, "", AssistantIconStyle.Render("● ")+strings.TrimPrefix(indented, "  ")+m.Spinner.View())
		}
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

	if status := m.renderRetryStatus(); status != "" {
		parts = append(parts, "", MutedStyle.Render(status))
	}

	parts = append(parts, "")

	if overlay != "" && overlayReplacesInput {
		parts = append(parts, overlay)
	} else if overlay != "" {
		if statusBar := m.RenderStatusBar(); statusBar != "" {
			parts = append(parts, statusBar, "")
		}
		appendInputArea()
		parts = append(parts, overlay)
	} else if m.AskUser != nil {
		parts = append(parts, renderAskUser(m.AskUser))
	} else if m.Permission != nil {
		parts = append(parts, renderPermission(m.Permission, m.Markdown))
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
		} else if fleet := m.renderFleetList(); fleet != "" {
			parts = append(parts, fleet)
		}
	}

	// While focused in the fleet list, the list owns the bottom region — the
	// context bar steps aside (it returns when focus goes back to the input).
	if !m.compActive && overlay == "" && m.AskUser == nil && !m.FleetFocus {
		parts = append(parts, m.RenderContextBar())
		parts = append(parts, "")
	}

	return strings.Join(parts, "\n")
}

// renderRetryStatus formats the live retry line with an integer-second countdown.
// Returns empty when no retry is in progress.
func (m *Model) renderRetryStatus() string {
	if m.RetryPrefix == "" {
		return ""
	}
	if m.RetryDeadline.IsZero() {
		return m.RetryPrefix + "..."
	}
	remain := time.Until(m.RetryDeadline)
	if remain <= 0 {
		return m.RetryPrefix + "..."
	}
	secs := int((remain + time.Second - 1) / time.Second)
	return fmt.Sprintf("%s in %ds...", m.RetryPrefix, secs)
}
