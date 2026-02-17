package provider

import (
	"context"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

type cfgModel struct {
	cfg llm.GenerationConfig
}

func (m *cfgModel) Generate(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{}, nil
}

func (m *cfgModel) GenerateStream(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent)
	close(ch)
	return ch, nil
}

func (m *cfgModel) SupportsTools() bool { return true }

func (m *cfgModel) GetConfig() *llm.GenerationConfig { return &m.cfg }

func TestApplyProviderDefaultsAnthropicClamp(t *testing.T) {
	t.Parallel()

	m := &cfgModel{cfg: llm.GenerationConfig{MaxTokens: 65536}}
	applyProviderDefaults("anthropic", "claude-sonnet-4-5-20250929", m)
	if m.cfg.MaxTokens != 64000 {
		t.Fatalf("max tokens = %d, want 64000", m.cfg.MaxTokens)
	}
}

func TestApplyProviderDefaultsAnthropicClampOpus(t *testing.T) {
	t.Parallel()

	m := &cfgModel{cfg: llm.GenerationConfig{MaxTokens: 65536}}
	applyProviderDefaults("anthropic", "claude-opus-4-6", m)
	if m.cfg.MaxTokens != 32000 {
		t.Fatalf("max tokens = %d, want 32000", m.cfg.MaxTokens)
	}
}

func TestApplyProviderDefaultsNonAnthropicUnchanged(t *testing.T) {
	t.Parallel()

	m := &cfgModel{cfg: llm.GenerationConfig{MaxTokens: 65536}}
	applyProviderDefaults("openai", "gpt-4.1", m)
	if m.cfg.MaxTokens != 65536 {
		t.Fatalf("max tokens = %d, want 65536", m.cfg.MaxTokens)
	}
}
