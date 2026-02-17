package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/voocel/agentcore"
)

type fakeModel struct {
	streamErr error
	resp      *agentcore.LLMResponse
}

func (f *fakeModel) Generate(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	if f.resp == nil {
		return nil, errors.New("no response")
	}
	return f.resp, nil
}

func (f *fakeModel) GenerateStream(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: f.resp.Message}
	close(ch)
	return ch, nil
}

func (f *fakeModel) SupportsTools() bool { return true }

func TestWrapStreamSafeFallsBackToGenerateOnStreamError(t *testing.T) {
	t.Parallel()

	inner := &fakeModel{
		streamErr: errors.New("stream unavailable"),
		resp: &agentcore.LLMResponse{
			Message: agentcore.Message{
				Role:       agentcore.RoleAssistant,
				Content:    []agentcore.ContentBlock{agentcore.TextBlock("ok")},
				StopReason: agentcore.StopReasonStop,
			},
		},
	}
	m := WrapStreamSafe(inner)
	ch, err := m.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("GenerateStream error: %v", err)
	}
	ev, ok := <-ch
	if !ok {
		t.Fatalf("expected one stream event")
	}
	if ev.Type != agentcore.StreamEventDone {
		t.Fatalf("event type = %s, want %s", ev.Type, agentcore.StreamEventDone)
	}
	if ev.Message.TextContent() != "ok" {
		t.Fatalf("message content = %q, want %q", ev.Message.TextContent(), "ok")
	}
}
