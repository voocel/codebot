package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/catalog"
	"github.com/voocel/litellm/gateway"
	"github.com/voocel/litellm/litellmtest"
	llmprovider "github.com/voocel/litellm/provider"
)

// capsProvider is a test provider stating caps.
type capsProvider struct {
	*litellmtest.Provider
	caps litellm.Capabilities
}

func (p capsProvider) Capabilities() litellm.Capabilities { return p.caps }

func modelWith(t *testing.T, caps *litellm.Capabilities) agentcore.Model {
	t.Helper()
	var p litellm.Provider = litellmtest.New()
	if caps != nil {
		p = capsProvider{litellmtest.New(), *caps}
	}
	client, err := litellm.New(p)
	if err != nil {
		t.Fatal(err)
	}
	return agentcore.Model{Client: client, Request: litellm.Request{Model: "m"}}
}

func TestEfforts(t *testing.T) {
	t.Parallel()

	for _, effort := range []string{"minimal", "auto", "High", " high "} {
		if ValidEffort(effort) {
			t.Errorf("%q accepted as a reasoning effort", effort)
		}
	}
	all := []string{"", "off", "low", "medium", "high", "xhigh", "max"}
	for reasoning, want := range map[*bool][]string{nil: all, new(true): all, new(false): {""}} {
		if got := ThinkingLevels(nil, reasoning); !slices.Equal(got, want) {
			t.Errorf("reasoning %v: levels %q, want %q", reasoning, got, want)
		}
	}
	// A provider offers only the settings it can send: MiMo takes no effort,
	// Grok cannot turn thinking off.
	for name, want := range map[string][]string{"deepseek": all, "mimo": {"", "off"}, "grok": {"", "low", "medium", "high", "xhigh", "max"}} {
		p, err := llmprovider.New(name, llmprovider.Config{APIKey: "k"})
		if err != nil {
			t.Fatal(err)
		}
		client, err := litellm.New(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := ThinkingLevels(client, new(true)); !slices.Equal(got, want) {
			t.Errorf("%s: levels %q, want %q", name, got, want)
		}
	}
	if Thinking("") != nil || !Thinking("off").Disabled || Thinking("high").Effort != "high" {
		t.Fatal("efforts map to the wrong request settings")
	}
}

// The cache key goes only where the provider takes one, and never into the
// model it was given.
func TestWithCacheKey(t *testing.T) {
	t.Parallel()

	m := modelWith(t, &litellm.Capabilities{ProviderOptions: []string{"prompt_cache_key"}})
	m.Request.ProviderOptions = litellm.ProviderOptions{"user": json.RawMessage(`"u"`)}
	keyed := WithCacheKey(m, "s1")
	if string(keyed.Request.ProviderOptions["prompt_cache_key"]) != `"s1"` || len(m.Request.ProviderOptions) != 1 {
		t.Fatalf("keyed %v, original %v", keyed.Request.ProviderOptions, m.Request.ProviderOptions)
	}
	if plain := WithCacheKey(modelWith(t, &litellm.Capabilities{}), "s1"); plain.Request.ProviderOptions != nil {
		t.Fatalf("a provider without the option got %v", plain.Request.ProviderOptions)
	}
}

func TestModelsLookup(t *testing.T) {
	t.Parallel()

	m := &Models{}
	for name, cap := range map[string]int{"claude-x": 1, "xai/grok-x": 2, "moonshot/kimi-x": 3, "gemini-x": 4} {
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
		{"another vendor's entry", ModelSpec{Provider: "gemini", Type: "gemini", Model: "gemini-x"}, 0},
		{"prefix of the provider name", ModelSpec{Provider: "moonshot", Type: "compat", Model: "kimi-x"}, 3},
		{"bare name behind compat", ModelSpec{Provider: "gateway", Type: "compat", Model: "claude-x"}, 1},
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
// ones, since Anthropic rejects requests without one, and the listed prices.
func TestModelFactoryAnthropic(t *testing.T) {
	var body struct {
		MaxTokens int `json:"max_tokens"`
	}
	var beta string
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
	pricing := &catalog.Pricing{Rates: catalog.Rates{Input: 3e-6, Output: 15e-6}}
	if err := models.catalog.Set("claude-x", catalog.Model{MaxOutputTokens: 64000, Pricing: pricing}); err != nil {
		t.Fatal(err)
	}
	factory := NewModelFactory(models)
	conn := llmprovider.Config{APIKey: "test-key", BaseURL: server.URL, Headers: map[string]string{"anthropic-beta": "beta-a"}}
	call := func(name string) agentcore.Model {
		t.Helper()
		m, err := factory(ModelSpec{Provider: "anthropic", Type: "anthropic", Model: name, Conn: conn})
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		req := m.Request
		req.Messages = []litellm.Message{litellm.UserText("hi")}
		if _, err := m.Client.Chat(context.Background(), req); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		return m
	}

	if m := call("claude-x"); body.MaxTokens != 64000 || beta != "beta-a" || !reflect.DeepEqual(m.Pricing, pricing) {
		t.Fatalf("max_tokens = %d, anthropic-beta = %q, pricing %+v", body.MaxTokens, beta, m.Pricing)
	}
	if m := call("claude-unlisted"); body.MaxTokens != fallbackMaxTokens || m.Pricing != nil {
		t.Fatalf("unlisted model: max_tokens = %d, pricing = %+v", body.MaxTokens, m.Pricing)
	}
}

// A gateway provider runs the model on the gateway, which knows codebot by
// its API key.
func TestModelFactoryGateway(t *testing.T) {
	var auth string
	var got *litellm.Request
	srv := httptest.NewServer(&gateway.Server{Route: func(r *http.Request, req *litellm.Request) (*litellm.Client, error) {
		auth, got = r.Header.Get("Authorization"), req
		return litellm.New(litellmtest.New(litellmtest.Text("ok")))
	}})
	defer srv.Close()

	if !IsSupportedType("gateway") {
		t.Fatal("the gateway type must be supported")
	}
	m, err := NewModelFactory(NewModels())(ModelSpec{
		Provider: "corp",
		Type:     "gateway",
		Model:    "claude-sonnet-4-6",
		Conn:     llmprovider.Config{APIKey: "sandbox-token", BaseURL: srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := m.Request
	req.Messages = []litellm.Message{litellm.UserText("hi")}
	req.Thinking = Thinking("high")
	resp, err := m.Client.Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "ok" || auth != "Bearer sandbox-token" || got.Model != "claude-sonnet-4-6" || got.Thinking.Effort != "high" {
		t.Fatalf("reply %q, auth %q, request %+v", resp.Text(), auth, got)
	}

	if _, err := NewModelFactory(NewModels())(ModelSpec{Provider: "corp", Type: "gateway", Model: "m"}); err == nil {
		t.Fatal("a gateway without a base URL must be refused")
	}
}
