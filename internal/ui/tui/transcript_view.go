package tui

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	reflowwrap "github.com/muesli/reflow/wrap"
)

// TranscriptView renders an agentcore.Event stream as a scrollable in-memory
// transcript. It is the read-only sibling of the leader's scrollback path in
// events.go, designed for the modal popup that lets the user observe a
// teammate's live activity.
//
// Why a separate renderer and not events.go?
//   - events.go writes to terminal scrollback via tea.Println (global stream,
//     cannot be split across multiple views).
//   - HandleAgentEvent reads/writes >10 Model fields (PendingTools, ToolHeaders,
//     RunStats, Streaming, …). Reusing it would require either dragging the
//     whole Model into the modal or extracting every helper that touches them.
//   - The modal scope is narrower: teammates can't enter plan mode, can't
//     spawn nested subagents, can't show ask_user dialogs. Most of events.go's
//     special-case branches don't apply.
//
// What IS shared with events.go: the rendering style constants and pure
// formatting helpers (RenderToolHeader, FormatToolResult, FormatToolOutput,
// FormatProgressLine, indentBlock, truncateRunes). The two views look the
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

	// Active tool calls keyed by ToolID. Set on EventToolExecStart, drained
	// on EventToolExecEnd. Hidden tools (IsHiddenToolCall) never enter.
	activeTools map[string]*transcriptToolState

	// status is the bottom-bar text, e.g. "● researcher · running · 3 tools".
	// The view writer sets it; the renderer just displays.
	status string

	// title is the top-bar text, e.g. "teammate: researcher".
	title string
}

type transcriptToolState struct {
	header      string
	deltaBuf    strings.Builder
	thinkingBuf strings.Builder
	outputBuf   strings.Builder
	args        []byte // raw args captured at Start; End events drop them
}

// NewTranscriptView returns an empty view. Call SetSize before View() —
// without it the viewport has zero dimensions and View() returns "".
func NewTranscriptView(title string) *TranscriptView {
	return &TranscriptView{
		vp:          viewport.New(0, 0),
		activeTools: make(map[string]*transcriptToolState),
		title:       title,
	}
}

// SetTitle updates the top-bar label. Title visibility affects how much
// vertical room the viewport gets, so the layout is recomputed.
func (t *TranscriptView) SetTitle(title string) {
	if t.title == title {
		return
	}
	t.title = title
	t.applyLayout()
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
// SetSize, SetTitle, and SetStatus stay in sync — earlier versions only
// recomputed in SetSize, so a SetStatus call after SetSize left the
// viewport oversized and overlapped the status bar by one row.
func (t *TranscriptView) applyLayout() {
	t.vp.Width = t.width
	reserved := 0
	if t.title != "" {
		reserved++
	}
	if t.status != "" {
		reserved++
	}
	t.vp.Height = max(0, t.height-reserved)
	t.repaint()
}

// HandleEvent updates state in response to one agentcore event. Mirrors the
// subset of events.go HandleAgentEvent that a teammate actually produces.
// After each call the viewport content is rebuilt so View() reflects the
// latest state; the cost is O(len(blocks)) per event which is fine for the
// target scale (hundreds of blocks per teammate run).
func (t *TranscriptView) HandleEvent(ev agentcore.Event) {
	switch ev.Type {
	case agentcore.EventAgentStart:
		// No state change; the caller usually flips status here.

	case agentcore.EventMessageStart:
		if ev.Message != nil && ev.Message.GetRole() == agentcore.RoleAssistant {
			t.isStream = true
			t.streaming.Reset()
			t.thinking.Reset()
		}

	case agentcore.EventMessageUpdate:
		if !t.isStream {
			break
		}
		if ev.Message != nil {
			if text := ev.Message.TextContent(); text != "" {
				t.streaming.Reset()
				t.streaming.WriteString(text)
			}
			if thinking := ev.Message.ThinkingContent(); thinking != "" {
				t.thinking.Reset()
				t.thinking.WriteString(thinking)
			}
		} else if ev.Delta != "" {
			t.streaming.WriteString(ev.Delta)
		}

	case agentcore.EventMessageEnd:
		if ev.Message == nil || ev.Message.GetRole() != agentcore.RoleAssistant {
			break
		}
		t.isStream = false
		content := strings.TrimSpace(ev.Message.TextContent())
		thinkingText := strings.TrimSpace(ev.Message.ThinkingContent())
		t.streaming.Reset()
		t.thinking.Reset()
		if content == "" && thinkingText == "" {
			break
		}
		t.appendAssistantBlock(thinkingText, content)

	case agentcore.EventToolExecStart:
		if IsHiddenToolCall(ev.Tool, ev.Args) {
			break
		}
		// Build the header eagerly so the End handler doesn't need to
		// re-render with potentially-dropped args.
		state := &transcriptToolState{
			header: ToolIconStyle.Render("● ") + RenderToolHeader(ev.Tool, ev.Args),
			args:   append([]byte(nil), ev.Args...),
		}
		t.activeTools[ev.ToolID] = state
		// Show the header immediately as a provisional block so the user
		// sees the tool is in progress; the End handler will replace it
		// with header+result.
		t.appendBlock(state.header)

	case agentcore.EventToolExecUpdate:
		if _, hidden := t.activeTools[ev.ToolID]; !hidden {
			// Either we missed Start (hidden tool) or the call is stale.
			break
		}
		state := t.activeTools[ev.ToolID]
		if ev.UpdateKind == agentcore.ToolExecUpdateProgress && ev.Progress != nil {
			switch ev.Progress.Kind {
			case agentcore.ProgressToolDelta:
				if ev.Progress.Delta != "" {
					state.deltaBuf.WriteString(ev.Progress.Delta)
				}
			case agentcore.ProgressThinking:
				if ev.Progress.Thinking != "" {
					state.thinkingBuf.Reset()
					state.thinkingBuf.WriteString(ev.Progress.Thinking)
				}
			default:
				if line := FormatProgressLine(ev.Progress); line != "" {
					state.outputBuf.WriteString(line)
					state.outputBuf.WriteByte('\n')
				}
			}
		}

	case agentcore.EventToolExecEnd:
		state, ok := t.activeTools[ev.ToolID]
		if !ok {
			// Was either hidden at Start or never tracked.
			break
		}
		delete(t.activeTools, ev.ToolID)

		header := state.header
		if ev.IsError {
			// Re-tint the bullet red, keep the header tail (args) intact.
			okBullet := ToolIconStyle.Render("● ")
			redBullet := ErrorIconStyle.Render("● ")
			if rest, hadBullet := strings.CutPrefix(header, okBullet); hadBullet {
				header = redBullet + rest
			} else {
				header = redBullet + RenderToolHeader(ev.Tool, state.args)
			}
		}

		body := t.renderToolBody(ev, state)
		block := header
		if body != "" {
			block = header + "\n" + body
		}
		// Replace the provisional header-only block (last appended by Start)
		// with the final block. If the user spawned multiple tools in
		// parallel, the order of "provisional headers" may not match End
		// order — find by header prefix to be safe.
		t.replaceProvisional(state.header, block)

	case agentcore.EventError:
		if ev.Err != nil && errors.Is(ev.Err, context.Canceled) {
			break
		}
		msg := "unknown error"
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		t.appendBlock(ErrorStyle.Render(wrapTextWidth("error: "+msg, t.bodyWidth())))

	case agentcore.EventAgentEnd:
		// Status line update is the caller's job (we don't know whether
		// AgentEnd was clean or aborted at this layer).
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

// renderToolBody picks the right formatter for a tool's End event. Mirrors
// the relevant branches of events.go but skips the leader-only paths
// (subagent card, plan-file suppression, write/edit diff specialisations) —
// teammates don't produce those.
func (t *TranscriptView) renderToolBody(ev agentcore.Event, _ *transcriptToolState) string {
	if ev.IsError {
		text := wrapTextWidth(FormatToolResult(ev.Result, true), t.bodyWidth()-4)
		return indentBlock(FormatToolOutput(text, ToolResultMaxLines, MutedStyle), 2)
	}
	text := wrapTextWidth(FormatToolResult(ev.Result, false), t.bodyWidth()-4)
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

	t.vp.SetContent(body.String())
}

// View renders the title + viewport + status into a single string sized to
// (width, height). Returns "" if the view has no room to draw.
func (t *TranscriptView) View() string {
	if t.width <= 0 || t.height <= 0 {
		return ""
	}
	pieces := make([]string, 0, 3)
	if t.title != "" {
		pieces = append(pieces, transcriptTitleStyle.Render(t.title))
	}
	pieces = append(pieces, t.vp.View())
	if t.status != "" {
		pieces = append(pieces, MutedStyle.Render(t.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, pieces...)
}

// ScrollUp / ScrollDown / GotoBottom proxy to the viewport. The repaint that
// follows event handling auto-scrolls when the user is already at the bottom
// (viewport's default behaviour with SetContent).
func (t *TranscriptView) ScrollUp(n int)   { t.vp.ScrollUp(n) }
func (t *TranscriptView) ScrollDown(n int) { t.vp.ScrollDown(n) }
func (t *TranscriptView) PageUp()          { t.vp.PageUp() }
func (t *TranscriptView) PageDown()        { t.vp.PageDown() }
func (t *TranscriptView) GotoBottom()      { t.vp.GotoBottom() }

// transcriptTitleStyle is the inline header style used at the top of the
// modal. Bold + foreground emphasis without a border keeps it visually
// distinct from the body without consuming extra vertical space.
var transcriptTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(Strong)

// bodyWidth returns the column budget the renderer uses for wrapping. Falls
// back to 80 before the first SetSize so unit tests have a sane default.
func (t *TranscriptView) bodyWidth() int {
	if t.width > 0 {
		return t.width
	}
	return 80
}

// wrapTextWidth is the model-free sibling of (*Model).wrapTextForIndent. The
// transcript view does not know its host model so it carries its own width.
// width <= 1 falls back to 79 (matches Model.wrapTextForIndent's behaviour
// to keep visual output identical).
func wrapTextWidth(content string, width int) string {
	if content == "" {
		return ""
	}
	if width <= 1 {
		width = 79
	}
	return strings.TrimRight(reflowwrap.String(content, width), "\n")
}
