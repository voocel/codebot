package tui

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	cbteam "github.com/voocel/codebot/internal/team"
)

func TestRenderTeammateMessage_IncludesSenderAndBody(t *testing.T) {
	t.Parallel()

	m := New(nil, "test-model")
	m.Width = 80
	out := m.renderTeammateMessage("alice", "I found the bug at line 42.")

	for _, want := range []string{"alice", "I found the bug at line 42."} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

// Pump-injected teammate messages arrive as RoleUser events. The handler must
// distinguish them from real user input (which is suppressed at MessageEnd
// because it's already echoed via RenderPromptOutput) and route them through
// the teammate renderer instead.
func TestHandleAgentEvent_TeammateUserMessageProducesPrintCmd(t *testing.T) {
	t.Parallel()

	m := New(nil, "test-model")
	m.Ready = true
	m.Width = 80

	envelope := cbteam.FormatTeammateAttachment("alice", "found it", "", "")
	_, cmd := m.HandleAgentEvent(agentcore.Event{
		Type: agentcore.EventMessageEnd,
		Message: agentcore.Message{
			Role:    agentcore.RoleUser,
			Content: []agentcore.ContentBlock{agentcore.TextBlock(envelope)},
		},
	})
	if cmd == nil {
		t.Fatal("expected non-nil cmd for teammate-wrapped user message")
	}
}

// Plain user messages already render via RenderPromptOutput at submit time.
// The MessageEnd handler must NOT re-emit them or scrollback double-prints.
func TestHandleAgentEvent_PlainUserMessageEmitsNothing(t *testing.T) {
	t.Parallel()

	m := New(nil, "test-model")
	m.Ready = true
	m.Width = 80

	_, cmd := m.HandleAgentEvent(agentcore.Event{
		Type: agentcore.EventMessageEnd,
		Message: agentcore.Message{
			Role:    agentcore.RoleUser,
			Content: []agentcore.ContentBlock{agentcore.TextBlock("hello there")},
		},
	})
	if cmd != nil {
		t.Errorf("expected nil cmd for plain user message (would double-print), got non-nil")
	}
}
