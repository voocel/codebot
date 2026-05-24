package tui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

// asstMsg builds an assistant message with the given plain text. Used by
// tests as a shorthand — TranscriptView itself only reads Role and
// TextContent / ThinkingContent.
func asstMsg(text string) agentcore.Message {
	return agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{{Type: agentcore.ContentText, Text: text}},
	}
}

func TestTranscriptView_AssistantMessageEndAppendsBlock(t *testing.T) {
	v := NewTranscriptView("teammate: alice")
	v.SetSize(80, 20)

	v.HandleEvent(agentcore.Event{Type: agentcore.EventMessageStart, Message: asstMsg("")})
	v.HandleEvent(agentcore.Event{Type: agentcore.EventMessageEnd, Message: asstMsg("hello world")})

	got := v.View()
	if !strings.Contains(got, "hello world") {
		t.Errorf("expected 'hello world' in view, got:\n%s", got)
	}
	// Title shows
	if !strings.Contains(got, "teammate: alice") {
		t.Errorf("expected title in view, got:\n%s", got)
	}
}

// While a message is streaming, the live text must already appear in View()
// before EventMessageEnd — that's the whole point of a live transcript.
func TestTranscriptView_StreamingTextIsVisibleBeforeEnd(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	v.HandleEvent(agentcore.Event{Type: agentcore.EventMessageStart, Message: asstMsg("")})
	v.HandleEvent(agentcore.Event{Type: agentcore.EventMessageUpdate, Delta: "partial"})

	got := v.View()
	if !strings.Contains(got, "partial") {
		t.Errorf("expected streaming delta in view, got:\n%s", got)
	}

	// After end the streaming buffer clears but the final text persists.
	v.HandleEvent(agentcore.Event{Type: agentcore.EventMessageEnd, Message: asstMsg("partial-final")})
	got = v.View()
	if !strings.Contains(got, "partial-final") {
		t.Errorf("expected final text in view, got:\n%s", got)
	}
}

func TestTranscriptView_ToolExecRendersHeaderAndResult(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	toolID := "t1"
	args := json.RawMessage(`{"path":"/tmp/x"}`)
	v.HandleEvent(agentcore.Event{
		Type:   agentcore.EventToolExecStart,
		Tool:   "read",
		ToolID: toolID,
		Args:   args,
	})
	// Header should appear immediately as a provisional block.
	if got := v.View(); !strings.Contains(got, "Read") {
		t.Errorf("expected tool header 'Read' in view after Start, got:\n%s", got)
	}

	v.HandleEvent(agentcore.Event{
		Type:   agentcore.EventToolExecEnd,
		Tool:   "read",
		ToolID: toolID,
		Result: json.RawMessage(`"file contents here"`),
	})
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

	toolID := "t1"
	args := json.RawMessage(`{}`)
	v.HandleEvent(agentcore.Event{
		Type:   agentcore.EventToolExecStart,
		Tool:   "bash",
		ToolID: toolID,
		Args:   args,
	})
	v.HandleEvent(agentcore.Event{
		Type:    agentcore.EventToolExecEnd,
		Tool:    "bash",
		ToolID:  toolID,
		Result:  json.RawMessage(`"command not found"`),
		IsError: true,
	})
	got := v.View()
	if !strings.Contains(got, "command not found") {
		t.Errorf("expected error result in view, got:\n%s", got)
	}
}

func TestTranscriptView_HiddenToolIsSilent(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	// task_* tools are filtered out — see IsHiddenToolCall.
	args := json.RawMessage(`{"title":"x"}`)
	v.HandleEvent(agentcore.Event{
		Type:   agentcore.EventToolExecStart,
		Tool:   "task_create",
		ToolID: "h1",
		Args:   args,
	})
	v.HandleEvent(agentcore.Event{
		Type:   agentcore.EventToolExecEnd,
		Tool:   "task_create",
		ToolID: "h1",
	})
	got := v.View()
	if strings.Contains(got, "task_create") || strings.Contains(got, "Task_create") {
		t.Errorf("hidden tool leaked into view:\n%s", got)
	}
}

func TestTranscriptView_ErrorEventAppendsBlock(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	v.HandleEvent(agentcore.Event{
		Type: agentcore.EventError,
		Err:  errors.New("boom"),
	})
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

	v.HandleEvent(agentcore.Event{
		Type: agentcore.EventError,
		Err:  context.Canceled,
	})
	got := v.View()
	if strings.Contains(got, "context canceled") || strings.Contains(got, "error:") {
		t.Errorf("cancellation should be silent, got:\n%s", got)
	}
}

func TestTranscriptView_EmptyMessageEndIsNoop(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	// Whitespace-only message — render path should skip empty content.
	v.HandleEvent(agentcore.Event{Type: agentcore.EventMessageStart, Message: asstMsg("")})
	v.HandleEvent(agentcore.Event{Type: agentcore.EventMessageEnd, Message: asstMsg("   ")})

	got := v.View()
	// Title + viewport: only the viewport's empty area should show up.
	if strings.Contains(got, "●") {
		t.Errorf("empty message produced a bullet, got:\n%s", got)
	}
}

func TestTranscriptView_UserMessageIgnored(t *testing.T) {
	v := NewTranscriptView("")
	v.SetSize(80, 20)

	userMsg := agentcore.Message{
		Role:    agentcore.RoleUser,
		Content: []agentcore.ContentBlock{{Type: agentcore.ContentText, Text: "hi from user"}},
	}
	v.HandleEvent(agentcore.Event{Type: agentcore.EventMessageEnd, Message: userMsg})

	got := v.View()
	if strings.Contains(got, "hi from user") {
		t.Errorf("user message leaked into teammate transcript:\n%s", got)
	}
}

func TestTranscriptView_StatusAndTitleSettable(t *testing.T) {
	v := NewTranscriptView("initial")
	v.SetSize(80, 20)
	v.SetTitle("updated title")
	v.SetStatus("● running")

	got := v.View()
	if !strings.Contains(got, "updated title") {
		t.Errorf("title not reflected, got:\n%s", got)
	}
	if !strings.Contains(got, "running") {
		t.Errorf("status not reflected, got:\n%s", got)
	}
}

// Regression: SetStatus called AFTER SetSize used to leave the viewport
// height unchanged, so its rendering would clobber the newly-introduced
// status row. The fix routes both setters through applyLayout, so the
// viewport gives up one row to make room.
func TestTranscriptView_SetStatusAfterSetSizeKeepsRoom(t *testing.T) {
	v := NewTranscriptView("t") // title set => reserves 1 row already
	v.SetSize(80, 10)
	before := v.vp.Height // expect 10 - 1 (title) = 9
	v.SetStatus("bottom hint")
	after := v.vp.Height // expect 10 - 2 (title + status) = 8
	if before != 9 {
		t.Errorf("vp.Height before SetStatus = %d, want 9", before)
	}
	if after != 8 {
		t.Errorf("vp.Height after SetStatus = %d, want 8 (one row reserved for status)", after)
	}
}

func TestTranscriptView_NoSizeRendersEmpty(t *testing.T) {
	v := NewTranscriptView("x")
	// SetSize never called.
	v.HandleEvent(agentcore.Event{Type: agentcore.EventMessageEnd, Message: asstMsg("hi")})

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

	v.HandleEvent(agentcore.Event{
		Type:   agentcore.EventToolExecStart,
		Tool:   "read",
		ToolID: "a",
		Args:   json.RawMessage(`{"path":"/a"}`),
	})
	v.HandleEvent(agentcore.Event{
		Type:   agentcore.EventToolExecStart,
		Tool:   "bash",
		ToolID: "b",
		Args:   json.RawMessage(`{"command":"ls"}`),
	})
	v.HandleEvent(agentcore.Event{
		Type:   agentcore.EventToolExecEnd,
		Tool:   "bash",
		ToolID: "b",
		Result: json.RawMessage(`"file1\nfile2"`),
	})
	v.HandleEvent(agentcore.Event{
		Type:   agentcore.EventToolExecEnd,
		Tool:   "read",
		ToolID: "a",
		Result: json.RawMessage(`"hello"`),
	})

	got := v.View()
	// Both tools should be visible with their results.
	if !strings.Contains(got, "file1") {
		t.Errorf("bash result missing, got:\n%s", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("read result missing, got:\n%s", got)
	}
}
