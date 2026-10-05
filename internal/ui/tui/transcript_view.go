package tui

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	reflowwrap "github.com/muesli/reflow/wrap"
	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/app"
)

// TranscriptView renders an agentcore.Event stream as a scrollable in-memory
// transcript. It is the read-only sibling of the main scrollback path in
// events.go, designed for the modal popup that lets the user observe a
// agent's live activity.
//
// Why a separate renderer and not events.go?
//   - events.go writes to terminal scrollback via tea.Println (global stream,
//     cannot be split across multiple views).
//   - HandleAgentEvent reads/writes >10 Model fields (PendingTools, ToolHeaders,
//     RunStats, Streaming, …). Reusing it would require either dragging the
//     whole Model into the modal or extracting every helper that touches them.
//   - The modal scope is narrower: sub-agents can't spawn nested subagents
//     or show ask_user dialogs. Most of events.go's special-case branches
//     don't apply.
//
// What IS shared with events.go: the rendering style constants and pure
// formatting helpers (RenderToolHeader, FormatToolResult, FormatToolOutput,
// indentBlock). The two views look the
// same because they call the same helpers — there is no parallel theme to
// drift.
type TranscriptView struct {
	width, height int

	vp viewport.Model

	// blocks is the list of completed top-level blocks (assistant reply,
	// tool result, error line). Each entry is already styled & wrapped to
	// the current width; on resize we rebuild by reconstructing from the
	// raw events would be ideal but we don't keep them, so we re-wrap
	// blocks by re-rendering. For now resize triggers a full rebuild from
	// blocks (lipgloss wrapping survives width changes because we re-set
	// viewport content; long lines stay long but viewport handles overflow).
	blocks []string

	// In-flight assistant message accumulator. Empty between turns.
	streaming strings.Builder
	thinking  strings.Builder
	isStream  bool

	// The provisional headers of the tool calls in flight, by call ID. Set on
	// ToolStart, drained on ToolEnd. Hidden tools (app.HiddenToolCall) never
	// enter.
	activeTools map[string]string

	// status is the bottom-bar text, e.g. "● researcher · running · 3 tools".
	// The view writer sets it; the renderer just displays.
	status string

	// title is the top-bar text, e.g. "agent: researcher".
	title string

	// liveBadge is a short prefix prepended to the status line — typically a
	// spinner frame while the agent is publishing, an idle glyph once it
	// has stopped. Updated per frame by the modal so the user sees motion.
	// Stored on the view (rather than passed into View()) so the existing
	// View() signature stays single-argument and tests don't need updating.
	liveBadge string
}

// NewTranscriptView returns an empty view. Call SetSize before View() —
// without it the viewport has zero dimensions and View() returns "".
func NewTranscriptView(title string) *TranscriptView {
	return &TranscriptView{
		vp:          viewport.New(0, 0),
		activeTools: make(map[string]string),
		title:       title,
	}
}

// SetStatus updates the bottom-bar label. Status visibility affects how
// much vertical room the viewport gets, so the layout is recomputed.
// Pass "" to clear.
func (t *TranscriptView) SetStatus(status string) {
	if t.status == status {
		return
	}
	t.status = status
	t.applyLayout()
}

// SetLiveBadge swaps the spinner/idle marker shown at the head of the
// status line. Cheap on purpose: no layout recompute, no repaint — only the
// next View() call picks up the new badge. Pass "" to hide.
func (t *TranscriptView) SetLiveBadge(badge string) {
	t.liveBadge = badge
}

// SetSize records new outer dimensions and recomputes the viewport area.
// Width 0 or height < 2 makes the view effectively invisible.
func (t *TranscriptView) SetSize(width, height int) {
	if t.width == width && t.height == height {
		return
	}
	t.width = width
	t.height = height
	t.applyLayout()
}

// applyLayout (re)computes the viewport's width/height from the current
// outer size and the presence of title/status rows. Centralised so
// SetSize and SetStatus stay in sync — earlier versions only
// recomputed in SetSize, so a SetStatus call after SetSize left the
// viewport oversized and overlapped the status bar by one row.
//
// Each chrome row carries its own blank-line spacer (title beneath itself,
// status above) so the body never bumps directly against the colored strip.
// reserved therefore counts 2 rows per visible chrome, not 1.
//
// SetLiveBadge piggybacks on the status row's reservation — the caller
// must set a non-empty status before showing a badge, otherwise the badge
// will overflow into the viewport area.
func (t *TranscriptView) applyLayout() {
	t.vp.Width = t.width
	reserved := 0
	if t.title != "" {
		reserved += 2
	}
	if t.status != "" {
		reserved += 2
	}
	t.vp.Height = max(0, t.height-reserved)
	t.repaint()
}

// HandleEvent updates state in response to one agentcore event. Mirrors the
// subset of events.go HandleAgentEvent that a sub-agent actually produces.
// After each call the viewport content is rebuilt so View() reflects the
// latest state; the cost is O(len(blocks)) per event which is fine for the
// target scale (hundreds of blocks per sub-agent run).
func (t *TranscriptView) HandleEvent(ev agentcore.Event) {
	switch e := ev.(type) {
	case agentcore.MessageStart:
		t.isStream = true
		t.streaming.Reset()
		t.thinking.Reset()

	case agentcore.MessageDelta:
		if !t.isStream {
			break
		}
		switch d := e.Event.(type) {
		case litellm.TextDelta:
			t.streaming.WriteString(d.Text)
		case litellm.ReasoningDelta:
			t.thinking.WriteString(d.Text)
		}

	case agentcore.Retry:
		// The failed response is discarded.
		t.isStream = false
		t.streaming.Reset()
		t.thinking.Reset()

	case agentcore.MessageEnd:
		if e.Message.Role != litellm.RoleAssistant {
			break
		}
		t.isStream = false
		t.streaming.Reset()
		t.thinking.Reset()
		t.appendAssistantBlock(strings.TrimSpace(e.Message.Reasoning()), strings.TrimSpace(e.Message.Text()))

	case agentcore.ToolStart:
		if app.HiddenToolCall(e.Call.Name, e.Call.Args) {
			break
		}
		// Show the header at once, so the call reads as in progress; the
		// End handler replaces it with header and result.
		header := ToolIconStyle.Render("● ") + RenderToolHeader(e.Call.Name, e.Call.Args)
		t.activeTools[e.Call.ID] = header
		t.appendBlock(header)

	case agentcore.ToolEnd:
		provisional, ok := t.activeTools[e.Call.ID]
		if !ok {
			break
		}
		delete(t.activeTools, e.Call.ID)

		header := provisional
		if e.Result.IsError {
			header = ErrorIconStyle.Render("● ") + strings.TrimPrefix(provisional, ToolIconStyle.Render("● "))
		}
		block := header
		if body := t.renderToolBody(e.Result); body != "" {
			block = header + "\n" + body
		}
		// Parallel calls end in any order, so the provisional block is found
		// by its header.
		t.replaceProvisional(provisional, block)

	case agentcore.RunEnd:
		// A cancelled run ends without the response under way.
		t.isStream = false
		if e.Err != nil && !errors.Is(e.Err, context.Canceled) {
			t.appendBlock(ErrorStyle.Render(wrapTextWidth(app.ErrorText(e.Err), t.bodyWidth())))
		}
	}

	t.repaint()
}

// appendAssistantBlock builds the styled assistant block (thinking + content)
// and pushes it onto blocks.
func (t *TranscriptView) appendAssistantBlock(thinkingText, content string) {
	var block strings.Builder
	if thinkingText != "" {
		wrapped := wrapTextWidth(thinkingText, t.bodyWidth())
		block.WriteString(ThinkingBodyStyle.Render("● " + wrapped))
		if content != "" {
			block.WriteString("\n\n")
		}
	}
	if content != "" {
		wrapped := wrapTextWidth(content, t.bodyWidth())
		block.WriteString(AssistantIconStyle.Render("● ") + wrapped)
	}
	if block.Len() > 0 {
		t.appendBlock(block.String())
	}
}

// renderToolBody renders a tool call's result as generic output: the
// per-tool renderers of events.go are for the main conversation.
func (t *TranscriptView) renderToolBody(res agentcore.Result) string {
	text := res.Text()
	if res.IsError {
		text = wrapTextWidth(FormatToolResult(text, true), t.bodyWidth()-4)
		return indentBlock(FormatToolOutput(text, ToolResultMaxLines, MutedStyle), 2)
	}
	text = wrapTextWidth(FormatToolResult(text, false), t.bodyWidth()-4)
	return indentBlock(FormatToolOutput(text, ToolResultMaxLines), 2)
}

// appendBlock pushes one rendered block onto the transcript. Empty blocks
// are dropped so the viewport doesn't accumulate blank lines from no-op
// events.
func (t *TranscriptView) appendBlock(block string) {
	if block == "" {
		return
	}
	t.blocks = append(t.blocks, block)
}

// replaceProvisional finds the most recent block that equals provisional
// (the header we appended at Start) and swaps it for finalBlock. If the
// provisional cannot be found (e.g. user scrolled and we somehow lost
// state), we append finalBlock so the result is still visible.
func (t *TranscriptView) replaceProvisional(provisional, finalBlock string) {
	for i := len(t.blocks) - 1; i >= 0; i-- {
		if t.blocks[i] == provisional {
			t.blocks[i] = finalBlock
			return
		}
	}
	t.appendBlock(finalBlock)
}

// repaint rebuilds the viewport content from blocks + live streaming/tool
// state. Cheap enough at our scale; if profiling ever shows it on top,
// switch to an incremental append model.
//
// Scroll-follow: when the viewport was already at the bottom we re-pin to
// the bottom after SetContent so new events stream in like a tail -f. If
// the user has scrolled up to read history we leave the offset alone — the
// "auto-follow only while at the bottom" pattern most terminal viewers use.
func (t *TranscriptView) repaint() {
	var body strings.Builder
	for i, b := range t.blocks {
		if i > 0 {
			body.WriteString("\n\n")
		}
		body.WriteString(b)
	}

	// Live assistant streaming
	if t.isStream {
		liveText := strings.TrimSpace(t.streaming.String())
		liveThinking := strings.TrimSpace(t.thinking.String())
		if liveThinking != "" || liveText != "" {
			if body.Len() > 0 {
				body.WriteString("\n\n")
			}
			if liveThinking != "" {
				body.WriteString(ThinkingBodyStyle.Render("● " + wrapTextWidth(liveThinking, t.bodyWidth())))
				if liveText != "" {
					body.WriteString("\n\n")
				}
			}
			if liveText != "" {
				body.WriteString(AssistantIconStyle.Render("● ") + wrapTextWidth(liveText, t.bodyWidth()))
			}
		}
	}

	wasAtBottom := t.vp.AtBottom()
	t.vp.SetContent(body.String())
	if wasAtBottom {
		t.vp.GotoBottom()
	}
}

// View renders the title + viewport + status into a single string sized to
// (width, height). Returns "" if the view has no room to draw.
//
// Each visible chrome row is followed (title) or preceded (status) by a
// blank spacer line so the colored title strip and the status text never
// touch the body content. applyLayout reserves the matching rows.
func (t *TranscriptView) View() string {
	if t.width <= 0 || t.height <= 0 {
		return ""
	}
	pieces := make([]string, 0, 5)
	if t.title != "" {
		pieces = append(pieces, TranscriptTitleStyle.Width(t.width).Render(t.title))
		pieces = append(pieces, "")
	}
	pieces = append(pieces, t.vp.View())
	if t.status != "" || t.liveBadge != "" {
		line := t.status
		if t.liveBadge != "" {
			if line != "" {
				line = t.liveBadge + "  " + line
			} else {
				line = t.liveBadge
			}
		}
		pieces = append(pieces, "", MutedStyle.Render(line))
	}
	return strings.Join(pieces, "\n")
}

// ScrollUp / ScrollDown / GotoBottom proxy to the viewport. The repaint that
// follows event handling auto-scrolls when the user is already at the bottom
// (viewport's default behaviour with SetContent).
func (t *TranscriptView) ScrollUp(n int)   { t.vp.ScrollUp(n) }
func (t *TranscriptView) ScrollDown(n int) { t.vp.ScrollDown(n) }
func (t *TranscriptView) PageUp()          { t.vp.PageUp() }
func (t *TranscriptView) PageDown()        { t.vp.PageDown() }
func (t *TranscriptView) GotoBottom()      { t.vp.GotoBottom() }

// bodyWidth returns the column budget the renderer uses for wrapping. Falls
// back to 80 before the first SetSize so unit tests have a sane default.
func (t *TranscriptView) bodyWidth() int {
	if t.width > 0 {
		return t.width
	}
	return 80
}

// wrapTextWidth hard-wraps content to width cells; a width too small to use
// falls back to 79.
func wrapTextWidth(content string, width int) string {
	if content == "" {
		return ""
	}
	if width <= 1 {
		width = 79
	}
	return strings.TrimRight(reflowwrap.String(content, width), "\n")
}
