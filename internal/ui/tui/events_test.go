package tui

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestHandleAgentEventLifecycleState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		setup  func(*Model)
		event  agentcore.Event
		assert func(*testing.T, *Model)
	}{
		{
			name:  "agent start sets running",
			event: agentcore.Event{Type: agentcore.EventAgentStart},
			assert: func(t *testing.T, m *Model) {
				t.Helper()
				if !m.Running {
					t.Fatal("expected Running = true")
				}
			},
		},
		{
			name: "agent end clears running",
			setup: func(m *Model) {
				m.Running = true
			},
			event: agentcore.Event{Type: agentcore.EventAgentEnd},
			assert: func(t *testing.T, m *Model) {
				t.Helper()
				if m.Running {
					t.Fatal("expected Running = false")
				}
			},
		},
		{
			name:  "turn start increments counter",
			event: agentcore.Event{Type: agentcore.EventTurnStart},
			assert: func(t *testing.T, m *Model) {
				t.Helper()
				if m.TurnCount != 1 {
					t.Fatalf("TurnCount = %d, want 1", m.TurnCount)
				}
			},
		},
		{
			name: "assistant message start enables stream mode",
			event: agentcore.Event{
				Type: agentcore.EventMessageStart,
				Message: agentcore.Message{
					Role: agentcore.RoleAssistant,
				},
			},
			assert: func(t *testing.T, m *Model) {
				t.Helper()
				if !m.IsStream {
					t.Fatal("expected IsStream = true")
				}
			},
		},
		{
			name: "tool exec start tracks pending tool",
			event: agentcore.Event{
				Type:      agentcore.EventToolExecStart,
				ToolID:    "t1",
				Tool:      "read",
				ToolLabel: "Read File",
			},
			assert: func(t *testing.T, m *Model) {
				t.Helper()
				if _, ok := m.PendingTools["t1"]; !ok {
					t.Fatal("expected PendingTools to contain t1")
				}
				if _, ok := m.ToolHeaders["t1"]; !ok {
					t.Fatal("expected ToolHeaders to contain t1")
				}
			},
		},
		{
			name: "tool exec end clears pending tool",
			setup: func(m *Model) {
				m.PendingTools["t1"] = "read"
				m.ToolOutputBuf["t1"] = nil
			},
			event: agentcore.Event{
				Type:   agentcore.EventToolExecEnd,
				ToolID: "t1",
				Tool:   "read",
			},
			assert: func(t *testing.T, m *Model) {
				t.Helper()
				if _, ok := m.PendingTools["t1"]; ok {
					t.Fatal("expected PendingTools to remove t1")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(nil, "test-model")
			m.Ready = true
			m.Width = 80
			if tc.setup != nil {
				tc.setup(m)
			}

			next, _ := m.HandleAgentEvent(tc.event)
			tc.assert(t, mustModel(t, next))
		})
	}
}

func TestHandleAgentEventMessageEndReturnsPrintCmd(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content []agentcore.ContentBlock
	}{
		{
			name: "text response",
			content: []agentcore.ContentBlock{
				agentcore.TextBlock("final text"),
			},
		},
		{
			name: "thinking and response",
			content: []agentcore.ContentBlock{
				agentcore.ThinkingBlock("thinking content"),
				agentcore.TextBlock("answer"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(nil, "test-model")
			m.Ready = true
			m.Width = 80
			m.IsStream = true
			m.Streaming.WriteString("draft")

			next, cmd := m.HandleAgentEvent(agentcore.Event{
				Type: agentcore.EventMessageEnd,
				Message: agentcore.Message{
					Role:    agentcore.RoleAssistant,
					Content: tc.content,
				},
			})
			nextModel := mustModel(t, next)
			if nextModel.IsStream {
				t.Fatal("expected IsStream = false after MessageEnd")
			}
			if cmd == nil {
				t.Fatal("expected non-nil cmd for assistant MessageEnd")
			}
		})
	}
}

func TestHandleAgentEventCachesThinkingDuringStream(t *testing.T) {
	t.Parallel()

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
	model2 := mustModel(t, m2)

	update := agentcore.Event{
		Type: agentcore.EventMessageUpdate,
		Message: agentcore.Message{
			Role: agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{
				agentcore.ThinkingBlock("intermediate thinking"),
			},
		},
	}
	m3, _ := model2.HandleAgentEvent(update)
	model3 := mustModel(t, m3)
	if got := strings.TrimSpace(model3.Thinking.String()); got != "intermediate thinking" {
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
	m4, _ := model3.HandleAgentEvent(end)
	model4 := mustModel(t, m4)
	if got := strings.TrimSpace(model4.Thinking.String()); got != "" {
		t.Fatalf("thinking cache not cleared, got %q", got)
	}
}

func TestHandleAgentEventProgressBuffers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		events []agentcore.Event
		assert func(*testing.T, *Model)
	}{
		{
			name: "delta appends reply buffer",
			events: []agentcore.Event{
				{
					Type:       agentcore.EventToolExecUpdate,
					ToolID:     "t1",
					UpdateKind: agentcore.ToolExecUpdateProgress,
					Progress: &agentcore.ProgressPayload{
						Kind:  agentcore.ProgressToolDelta,
						Agent: "worker",
						Delta: "partial answer",
					},
				},
			},
			assert: func(t *testing.T, m *Model) {
				t.Helper()
				if got := m.ToolDeltaBuf["t1"].String(); got != "partial answer" {
					t.Fatalf("delta buffer = %q, want %q", got, "partial answer")
				}
			},
		},
		{
			name: "thinking keeps latest value",
			events: []agentcore.Event{
				{
					Type:       agentcore.EventToolExecUpdate,
					ToolID:     "t1",
					UpdateKind: agentcore.ToolExecUpdateProgress,
					Progress: &agentcore.ProgressPayload{
						Kind:     agentcore.ProgressThinking,
						Agent:    "worker",
						Thinking: "first thought",
					},
				},
				{
					Type:       agentcore.EventToolExecUpdate,
					ToolID:     "t1",
					UpdateKind: agentcore.ToolExecUpdateProgress,
					Progress: &agentcore.ProgressPayload{
						Kind:     agentcore.ProgressThinking,
						Agent:    "worker",
						Thinking: "second thought",
					},
				},
			},
			assert: func(t *testing.T, m *Model) {
				t.Helper()
				if got := m.ToolThinkingBuf["t1"].String(); got != "second thought" {
					t.Fatalf("thinking buffer = %q, want %q", got, "second thought")
				}
			},
		},
		{
			name: "summary appends accumulated output",
			events: []agentcore.Event{
				{
					Type:       agentcore.EventToolExecUpdate,
					ToolID:     "t1",
					UpdateKind: agentcore.ToolExecUpdateProgress,
					Progress: &agentcore.ProgressPayload{
						Kind:    agentcore.ProgressSummary,
						Summary: "bash line",
					},
				},
			},
			assert: func(t *testing.T, m *Model) {
				t.Helper()
				out := m.ToolOutputBuf["t1"].String()
				for _, want := range []string{"thinking thinking text", "reply reply text", "bash line"} {
					if !strings.Contains(out, want) {
						t.Fatalf("expected output buffer to contain %q, got %q", want, out)
					}
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(nil, "test-model")
			m.Ready = true
			m.Width = 80
			m.ToolOutputBuf["t1"] = &strings.Builder{}
			m.ToolDeltaBuf["t1"] = &strings.Builder{}
			m.ToolThinkingBuf["t1"] = &strings.Builder{}

			if tc.name == "summary appends accumulated output" {
				m.ToolDeltaBuf["t1"].WriteString("reply text")
				m.ToolThinkingBuf["t1"].WriteString("thinking text")
			}

			current := m
			for _, ev := range tc.events {
				next, _ := current.HandleAgentEvent(ev)
				current = mustModel(t, next)
			}
			tc.assert(t, current)
		})
	}
}
