package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestHandleAgentEventStartSetsRunning(t *testing.T) {
	m := New(nil, "test-model")
	m.Ready = true

	next, _ := m.HandleAgentEvent(agentcore.Event{Type: agentcore.EventAgentStart})
	if !next.Running {
		t.Fatal("expected Running = true after EventAgentStart")
	}
}

func TestHandleAgentEventEndClearsRunning(t *testing.T) {
	m := New(nil, "test-model")
	m.Ready = true
	m.Running = true

	next, _ := m.HandleAgentEvent(agentcore.Event{Type: agentcore.EventAgentEnd})
	if next.Running {
		t.Fatal("expected Running = false after EventAgentEnd")
	}
}

func TestHandleAgentEventTurnStartIncrements(t *testing.T) {
	m := New(nil, "test-model")
	m.Ready = true

	next, _ := m.HandleAgentEvent(agentcore.Event{Type: agentcore.EventTurnStart})
	if next.TurnCount != 1 {
		t.Fatalf("TurnCount = %d, want 1", next.TurnCount)
	}
}

func TestHandleAgentEventMessageStartSetsStream(t *testing.T) {
	m := New(nil, "test-model")
	m.Ready = true

	ev := agentcore.Event{
		Type: agentcore.EventMessageStart,
		Message: agentcore.Message{
			Role: agentcore.RoleAssistant,
		},
	}
	next, _ := m.HandleAgentEvent(ev)
	if !next.IsStream {
		t.Fatal("expected IsStream = true after assistant MessageStart")
	}
}

func TestHandleAgentEventMessageEndReturnsPrintCmd(t *testing.T) {
	m := New(nil, "test-model")
	m.Ready = true
	m.Width = 80
	m.IsStream = true
	m.Streaming.WriteString("test response")

	ev := agentcore.Event{
		Type: agentcore.EventMessageEnd,
		Message: agentcore.Message{
			Role: agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{
				agentcore.TextBlock("final text"),
			},
		},
	}

	next, cmd := m.HandleAgentEvent(ev)
	if next.IsStream {
		t.Fatal("expected IsStream = false after MessageEnd")
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd (printBlock) for assistant MessageEnd")
	}
}

func TestHandleAgentEventThinkingReturnsPrintCmd(t *testing.T) {
	m := New(nil, "test-model")
	m.Ready = true
	m.Width = 80
	m.IsStream = true
	m.Streaming.WriteString("draft")

	ev := agentcore.Event{
		Type: agentcore.EventMessageEnd,
		Message: agentcore.Message{
			Role: agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{
				agentcore.ThinkingBlock("thinking content"),
				agentcore.TextBlock("answer"),
			},
		},
	}

	next, cmd := m.HandleAgentEvent(ev)
	if next.IsStream {
		t.Fatal("expected IsStream = false after MessageEnd with thinking")
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd (printBlock) for thinking + response")
	}
}

func TestHandleAgentEventCachesThinkingDuringStream(t *testing.T) {
	m := New(nil, "test-model")
	m.Ready = true
	m.Width = 80

	start := agentcore.Event{
		Type: agentcore.EventMessageStart,
		Message: agentcore.Message{
			Role: agentcore.RoleAssistant,
		},
	}
	m2, _ := m.HandleAgentEvent(start)

	update := agentcore.Event{
		Type: agentcore.EventMessageUpdate,
		Message: agentcore.Message{
			Role: agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{
				agentcore.ThinkingBlock("intermediate thinking"),
			},
		},
	}
	m3, _ := m2.HandleAgentEvent(update)
	if got := strings.TrimSpace(m3.Thinking.String()); got != "intermediate thinking" {
		t.Fatalf("thinking cache = %q, want %q", got, "intermediate thinking")
	}

	end := agentcore.Event{
		Type: agentcore.EventMessageEnd,
		Message: agentcore.Message{
			Role: agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{
				agentcore.TextBlock("final answer"),
			},
		},
	}
	m4, _ := m3.HandleAgentEvent(end)
	if got := strings.TrimSpace(m4.Thinking.String()); got != "" {
		t.Fatalf("thinking cache not cleared, got %q", got)
	}
}

func TestHandleAgentEventToolExecStartAddsPending(t *testing.T) {
	m := New(nil, "test-model")
	m.Ready = true
	m.Width = 80

	ev := agentcore.Event{
		Type:      agentcore.EventToolExecStart,
		ToolID:    "t1",
		Tool:      "read",
		ToolLabel: "Read File",
	}

	next, _ := m.HandleAgentEvent(ev)
	if _, ok := next.PendingTools["t1"]; !ok {
		t.Fatal("expected PendingTools to contain t1")
	}
	// Header is buffered in ToolHeaders (printed together with result at ToolExecEnd).
	if _, ok := next.ToolHeaders["t1"]; !ok {
		t.Fatal("expected ToolHeaders to contain t1")
	}
}

func TestHandleAgentEventToolExecEndRemovesPending(t *testing.T) {
	m := New(nil, "test-model")
	m.Ready = true
	m.Width = 80
	m.PendingTools["t1"] = "read"
	m.ToolOutputBuf["t1"] = nil

	ev := agentcore.Event{
		Type:   agentcore.EventToolExecEnd,
		ToolID: "t1",
		Tool:   "read",
	}

	next, _ := m.HandleAgentEvent(ev)
	if _, ok := next.PendingTools["t1"]; ok {
		t.Fatal("expected PendingTools to not contain t1 after ToolExecEnd")
	}
}

func TestHandleAgentEventErrorReturnsPrintCmd(t *testing.T) {
	m := New(nil, "test-model")
	m.Ready = true

	ev := agentcore.Event{
		Type: agentcore.EventError,
		Err:  fmt.Errorf("something broke"),
	}

	_, cmd := m.HandleAgentEvent(ev)
	if cmd == nil {
		t.Fatal("expected non-nil cmd for error event")
	}
}
