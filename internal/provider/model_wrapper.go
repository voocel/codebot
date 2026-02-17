package provider

import (
	"context"

	"github.com/voocel/agentcore"
)

// WrapStreamSafe normalizes streaming behavior:
// when GenerateStream fails, it falls back to Generate and emits a synthetic done event.
func WrapStreamSafe(m agentcore.ChatModel) agentcore.ChatModel {
	if m == nil {
		return nil
	}
	return &streamSafeModel{inner: m}
}

type streamSafeModel struct {
	inner agentcore.ChatModel
}

func (m *streamSafeModel) Generate(
	ctx context.Context,
	messages []agentcore.Message,
	tools []agentcore.ToolSpec,
	opts ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return m.inner.Generate(ctx, messages, tools, opts...)
}

func (m *streamSafeModel) GenerateStream(
	ctx context.Context,
	messages []agentcore.Message,
	tools []agentcore.ToolSpec,
	opts ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	ch, err := m.inner.GenerateStream(ctx, messages, tools, opts...)
	if err == nil {
		return ch, nil
	}

	resp, genErr := m.inner.Generate(ctx, messages, tools, opts...)
	if genErr != nil {
		return nil, genErr
	}

	out := make(chan agentcore.StreamEvent, 1)
	go func() {
		defer close(out)
		out <- agentcore.StreamEvent{
			Type:       agentcore.StreamEventDone,
			Message:    resp.Message,
			StopReason: resp.Message.StopReason,
		}
	}()
	return out, nil
}

func (m *streamSafeModel) SupportsTools() bool {
	return m.inner.SupportsTools()
}

// ProviderName forwards provider name when available.
func (m *streamSafeModel) ProviderName() string {
	if pn, ok := m.inner.(agentcore.ProviderNamer); ok {
		return pn.ProviderName()
	}
	return ""
}
