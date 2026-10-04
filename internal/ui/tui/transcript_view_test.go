package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// asstMsg builds an assistant message with the given plain text. Used by
// tests as a shorthand — TranscriptView itself only reads Role, Text and
// Reasoning.
func asstMsg(text string) agentcore.Message {
	return agentcore.Message{Role: litellm.RoleAssistant, Blocks: []litellm.Block{litellm.Text(text)}}
}

func TestTranscriptView_AssistantMessageEndAppendsBlock(t *testing.T) {
	v := NewTranscriptView("agent: alice")
	v.SetSize(80, 20)

	v.HandleEvent(agentcore.MessageStart{})
	v.HandleEvent(agentcore.MessageEnd{Message: asstMsg("hello world")})

	got := v.View()
	if !strings.Contains(got, "hello world") {
		t.Errorf("expected 'hello world' in view, got:\n%s", got)
	}
	// Title shows
	if !strings.Contains(got, "agent: alice") {
		t.Errorf("expected title in view, got:\n%s", got)
	}
}

// While a message is streaming, the live text must already appear in View()
// before MessageEnd — that's the whole point of a live transcript.
func TestTranscriptView_StreamingTextIsVisibleBeforeEnd(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	v.HandleEvent(agentcore.MessageStart{})
	v.HandleEvent(agentcore.MessageDelta{Event: litellm.ReasoningDelta{Text: "mulling"}})
	v.HandleEvent(agentcore.MessageDelta{Event: litellm.TextDelta{Text: "part"}})
	v.HandleEvent(agentcore.MessageDelta{Event: litellm.TextDelta{Text: "ial"}})

	got := v.View()
	if !strings.Contains(got, "partial") || !strings.Contains(got, "mulling") {
		t.Errorf("expected streamed text and thinking in view, got:\n%s", got)
	}

	// After end the streaming buffer clears but the final text persists.
	v.HandleEvent(agentcore.MessageEnd{Message: asstMsg("partial-final")})
	got = v.View()
	if !strings.Contains(got, "partial-final") {
		t.Errorf("expected final text in view, got:\n%s", got)
	}
}

// A retry discards the response streamed so far.
func TestTranscriptView_RetryDiscardsStream(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	v.HandleEvent(agentcore.MessageStart{})
	v.HandleEvent(agentcore.MessageDelta{Event: litellm.TextDelta{Text: "doomed"}})
	v.HandleEvent(agentcore.Retry{Attempt: 2})

	if got := v.View(); strings.Contains(got, "doomed") {
		t.Errorf("retry kept the failed response, got:\n%s", got)
	}
}

func TestTranscriptView_ToolExecRendersHeaderAndResult(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	read := call("t1", "read", `{"path":"/tmp/x"}`)
	v.HandleEvent(agentcore.ToolStart{Call: read})
	// Header should appear immediately as a provisional block.
	if got := v.View(); !strings.Contains(got, "Read") {
		t.Errorf("expected tool header 'Read' in view after Start, got:\n%s", got)
	}

	v.HandleEvent(agentcore.ToolEnd{Call: read, Result: agentcore.TextResult("file contents here")})
	got := v.View()
	if !strings.Contains(got, "Read") {
		t.Errorf("expected tool header in view after End, got:\n%s", got)
	}
	if !strings.Contains(got, "file contents here") {
		t.Errorf("expected tool result in view, got:\n%s", got)
	}
}

func TestTranscriptView_ToolErrorRecolorsBullet(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	bash := call("t1", "bash", `{}`)
	v.HandleEvent(agentcore.ToolStart{Call: bash})
	v.HandleEvent(agentcore.ToolEnd{Call: bash, Result: agentcore.ErrorResult("command not found")})
	got := v.View()
	if !strings.Contains(got, "command not found") {
		t.Errorf("expected error result in view, got:\n%s", got)
	}
	if !strings.Contains(got, ErrorIconStyle.Render("● ")) {
		t.Errorf("expected the failed call's bullet in the error style, got:\n%s", got)
	}
}

func TestTranscriptView_HiddenToolIsSilent(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	// todo_write is filtered out — see app.HiddenToolCall.
	todo := call("h1", "todo_write", `{"todos":[]}`)
	v.HandleEvent(agentcore.ToolStart{Call: todo})
	v.HandleEvent(agentcore.ToolEnd{Call: todo, Result: agentcore.TextResult("ok")})
	got := v.View()
	if strings.Contains(got, "todo_write") || strings.Contains(got, "Update Todos") {
		t.Errorf("hidden tool leaked into view:\n%s", got)
	}
}

func TestTranscriptView_ErrorEventAppendsBlock(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	v.HandleEvent(agentcore.RunEnd{Reason: agentcore.EndError, Err: errors.New("boom")})
	got := v.View()
	if !strings.Contains(got, "boom") {
		t.Errorf("expected error in view, got:\n%s", got)
	}
}

// context.Canceled is a normal abort signal — not surfaced as an error
// block so the modal stays quiet on Esc/quit.
func TestTranscriptView_ErrorEventSuppressesCancellation(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	v.HandleEvent(agentcore.RunEnd{Reason: agentcore.EndAborted, Err: context.Canceled})
	got := v.View()
	if strings.Contains(got, "context canceled") || strings.Contains(got, "error:") {
		t.Errorf("cancellation should be silent, got:\n%s", got)
	}
}

func TestTranscriptView_EmptyMessageEndIsNoop(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	// Whitespace-only message — render path should skip empty content.
	v.HandleEvent(agentcore.MessageStart{})
	v.HandleEvent(agentcore.MessageEnd{Message: asstMsg("   ")})

	got := v.View()
	// Title + viewport: only the viewport's empty area should show up.
	if strings.Contains(got, "●") {
		t.Errorf("empty message produced a bullet, got:\n%s", got)
	}
}

func TestTranscriptView_UserMessageIgnored(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	v.HandleEvent(agentcore.MessageEnd{Message: agentcore.UserText("hi from user")})

	got := v.View()
	if strings.Contains(got, "hi from user") {
		t.Errorf("user message leaked into agent transcript:\n%s", got)
	}
}

func TestTranscriptView_StatusAndTitleShown(t *testing.T) {
	v := NewTranscriptView("agent: researcher")
	v.SetSize(80, 20)
	v.SetStatus("● running")

	got := v.View()
	if !strings.Contains(got, "agent: researcher") {
		t.Errorf("title not reflected, got:\n%s", got)
	}
	if !strings.Contains(got, "running") {
		t.Errorf("status not reflected, got:\n%s", got)
	}
}

// Regression: SetStatus called AFTER SetSize used to leave the viewport
// height unchanged, so its rendering would clobber the newly-introduced
// status row. The fix routes both setters through applyLayout, so the
// viewport gives up rows to make room. Each chrome row (title, status)
// pulls 2 lines — the row itself plus a blank spacer above/below.
func TestTranscriptView_SetStatusAfterSetSizeKeepsRoom(t *testing.T) {
	v := NewTranscriptView("t") // title set => reserves title + spacer = 2 rows
	v.SetSize(80, 12)
	before := v.vp.Height // 12 - 2 = 10
	v.SetStatus("bottom hint")
	after := v.vp.Height // 12 - 2 (title) - 2 (status) = 8
	if before != 10 {
		t.Errorf("vp.Height before SetStatus = %d, want 10", before)
	}
	if after != 8 {
		t.Errorf("vp.Height after SetStatus = %d, want 8 (status + spacer reserved)", after)
	}
}

func TestTranscriptView_NoSizeRendersEmpty(t *testing.T) {
	v := NewTranscriptView("x")
	// SetSize never called.
	v.HandleEvent(agentcore.MessageEnd{Message: asstMsg("hi")})

	if got := v.View(); got != "" {
		t.Errorf("expected empty view before SetSize, got: %q", got)
	}
}

// Several tool calls in flight at once should each get their own
// header → header+result transition. This exercises the
// replaceProvisional path with multiple matches.
func TestTranscriptView_ParallelToolCalls(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 30)

	read, bash := call("a", "read", `{"path":"/a"}`), call("b", "bash", `{"command":"ls"}`)
	v.HandleEvent(agentcore.ToolStart{Call: read})
	v.HandleEvent(agentcore.ToolStart{Call: bash})
	v.HandleEvent(agentcore.ToolEnd{Call: bash, Result: agentcore.TextResult("file1\nfile2")})
	v.HandleEvent(agentcore.ToolEnd{Call: read, Result: agentcore.TextResult("hello")})

	got := v.View()
	// Both tools should be visible with their results.
	if !strings.Contains(got, "file1") {
		t.Errorf("bash result missing, got:\n%s", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("read result missing, got:\n%s", got)
	}
}
