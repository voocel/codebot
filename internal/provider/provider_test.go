package provider

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/litellm/catalog"
	"github.com/voocel/litellm/providers"
)

type stubModel struct{}

func (m *stubModel) Generate(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{}, nil
}

func (m *stubModel) GenerateStream(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent)
	close(ch)
	return ch, nil
}

func (m *stubModel) SupportsTools() bool { return true }

type thinkingCapsModel struct {
	stubModel
	effort bool
}

func (m *thinkingCapsModel) Capabilities() (llm.Capabilities, bool) {
	return llm.Capabilities{Thinking: true, DisableThinking: true, ThinkingEffort: m.effort}, true
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
}

func TestThinkingLevelsForModelFiltersMinimal(t *testing.T) {
	t.Parallel()

	model := &thinkingCapsModel{effort: true}
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

func TestModelsLookup(t *testing.T) {
	t.Parallel()

	m := &Models{}
	for name, cap := range map[string]int{"claude-x": 1, "xai/grok-x": 2, "moonshot/kimi-x": 3} {
		if err := m.catalog.Set(name, catalog.Model{MaxOutputTokens: cap}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name string
		spec ModelSpec
		want int
	}{
		{"unprefixed name", ModelSpec{Provider: "anthropic", Type: "anthropic", Model: "claude-x"}, 1},
		{"vendor prefix of the type", ModelSpec{Provider: "my-grok", Type: "grok", Model: "grok-x"}, 2},
		{"prefix of the provider name", ModelSpec{Provider: "moonshot", Type: "compat", Model: "kimi-x"}, 3},
		{"unknown vendor", ModelSpec{Provider: "gateway", Type: "compat", Model: "kimi-x"}, 0},
	} {
		facts, ok := m.Lookup(tt.spec)
		if ok != (tt.want != 0) || facts.MaxOutputTokens != tt.want {
			t.Errorf("%s: Lookup = %+v, %v; want cap %d", tt.name, facts, ok, tt.want)
		}
	}
}

func TestNewModelsLoadsSnapshot(t *testing.T) {
	t.Parallel()

	facts, ok := NewModels().Lookup(ModelSpec{Provider: "anthropic", Type: "anthropic", Model: "claude-sonnet-4-5"})
	if !ok || facts.MaxInputTokens <= 0 || facts.MaxOutputTokens <= 0 || facts.Pricing == nil {
		t.Fatalf("snapshot facts = %+v, %v", facts, ok)
	}
}

// Anthropic models carry the listed output cap, or a fallback for unlisted
// ones, since Anthropic rejects requests without one; listed prices cost
// each call.
func TestModelFactoryAnthropic(t *testing.T) {
	var (
		body struct {
			MaxTokens int `json:"max_tokens"`
		}
		beta string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta = r.Header.Get("Anthropic-Beta")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "msg_test",
			"type":        "message",
			"role":        "assistant",
			"model":       "claude-x",
			"content":     []map[string]any{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 2},
		})
	}))
	defer server.Close()

	models := &Models{}
	pricing := &catalog.Pricing{InputCostPerToken: 3e-6, OutputCostPerToken: 15e-6}
	if err := models.catalog.Set("claude-x", catalog.Model{MaxOutputTokens: 64000, Pricing: pricing}); err != nil {
		t.Fatal(err)
	}
	factory := NewModelFactory(models)
	conn := providers.Config{APIKey: "test-key", BaseURL: server.URL, Headers: map[string]string{"anthropic-beta": "beta-a"}}
	generate := func(model string) *agentcore.Usage {
		t.Helper()
		m, err := factory(ModelSpec{Provider: "anthropic", Type: "anthropic", Model: model, Conn: conn})
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		resp, err := m.Generate(context.Background(), []agentcore.Message{
			{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("hi")}},
		}, nil)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		return resp.Message.Usage
	}

	usage := generate("claude-x")
	if body.MaxTokens != 64000 || beta != "beta-a" {
		t.Fatalf("max_tokens = %d, anthropic-beta = %q; want 64000, beta-a", body.MaxTokens, beta)
	}
	if usage.Cost == nil || math.Abs(usage.Cost.Total-60e-6) > 1e-12 {
		t.Fatalf("cost = %+v, want total 6e-5", usage.Cost)
	}

	usage = generate("claude-unlisted")
	if body.MaxTokens != anthropicFallbackMaxTokens || usage.Cost != nil {
		t.Fatalf("unlisted model: max_tokens = %d, cost = %+v", body.MaxTokens, usage.Cost)
	}
}
