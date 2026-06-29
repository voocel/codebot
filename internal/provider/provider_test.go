package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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

type thinkingCapsModel struct {
	cfgModel
	efforts []agentcore.ThinkingLevel
}

func (m *thinkingCapsModel) Capabilities() llm.Capabilities {
	return llm.Capabilities{
		Thinking: llm.ThinkingCapabilities{
			Supported: llm.SupportYes,
			Disable:   llm.SupportYes,
			Efforts:   m.efforts,
		},
	}
}

func TestReasoningEffortMinimalIsNotUserSelectable(t *testing.T) {
	t.Parallel()

	if IsValidThinkingLevel("minimal") {
		t.Fatal("minimal should not be accepted as a user-selectable reasoning effort")
	}
	if IsValidThinkingLevel("auto") {
		t.Fatal("auto should not be accepted as a user-selectable reasoning effort")
	}
	if IsValidThinkingLevel("High") || IsValidThinkingLevel(" high ") {
		t.Fatal("reasoning effort values must match exactly")
	}
	if slices.Contains(ThinkingLevelOrder, "minimal") {
		t.Fatalf("thinking order contains minimal: %v", ThinkingLevelOrder)
	}

	reg := NewModelRegistry()
	if levels := reg.AvailableThinkingLevels("unknown-model"); slices.Contains(levels, "minimal") {
		t.Fatalf("unknown model levels contain minimal: %v", levels)
	}
}

func TestThinkingLevelsForModelFiltersMinimal(t *testing.T) {
	t.Parallel()

	model := &thinkingCapsModel{efforts: []agentcore.ThinkingLevel{
		agentcore.ThinkingMinimal,
		agentcore.ThinkingLow,
		agentcore.ThinkingHigh,
	}}
	levels := ThinkingLevelsForModel(model)
	if slices.Contains(levels, "minimal") {
		t.Fatalf("thinking levels contain minimal: %v", levels)
	}
	for _, want := range []string{"", "off", "low", "high"} {
		if !slices.Contains(levels, want) {
			t.Fatalf("thinking levels missing %q: %v", want, levels)
		}
	}

	if got, ok := ResolveThinkingLevel(model, "minimal"); ok || got != "" {
		t.Fatalf("ResolveThinkingLevel(minimal) = %q, %v; want auto, false", got, ok)
	}
	if got, ok := ResolveThinkingLevel(model, "auto"); ok || got != "" {
		t.Fatalf("ResolveThinkingLevel(auto) = %q, %v; want auto, false", got, ok)
	}
}

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

	m := &cfgModel{cfg: llm.GenerationConfig{MaxTokens: 200000}}
	applyProviderDefaults("anthropic", "claude-opus-4-6", m)
	if m.cfg.MaxTokens != 128000 {
		t.Fatalf("max tokens = %d, want 128000", m.cfg.MaxTokens)
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

func TestApplyProviderDefaultsAnthropicUnknownFallback(t *testing.T) {
	t.Parallel()

	m := &cfgModel{cfg: llm.GenerationConfig{MaxTokens: 65536}}
	applyProviderDefaults("anthropic", "claude-unknown-next", m)
	if m.cfg.MaxTokens != 32000 {
		t.Fatalf("max tokens = %d, want 32000", m.cfg.MaxTokens)
	}
}

func TestCreateModelPassesProviderExtraHeaders(t *testing.T) {
	var gotUserAgent, gotCustomHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		gotCustomHeader = r.Header.Get("X-Custom-Client")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1,
			"model":   "gpt-test",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "ok",
					},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer server.Close()

	model, err := CreateModel("openai", "gpt-test", "test-key", server.URL, map[string]any{
		"user_agent": "codebot-test/1.0",
		"headers": map[string]string{
			"X-Custom-Client": "codebot",
		},
	})
	if err != nil {
		t.Fatalf("CreateModel: %v", err)
	}

	_, err = model.Generate(context.Background(), []agentcore.Message{
		{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("hi")}},
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gotUserAgent != "codebot-test/1.0" {
		t.Fatalf("User-Agent = %q, want codebot-test/1.0", gotUserAgent)
	}
	if gotCustomHeader != "codebot" {
		t.Fatalf("X-Custom-Client = %q, want codebot", gotCustomHeader)
	}
}
