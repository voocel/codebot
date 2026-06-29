package config

import (
	"errors"
	"testing"

	"github.com/voocel/codebot/internal/diag"
)

func TestMergeSettingsProviderAPI(t *testing.T) {
	base := Settings{
		Providers: map[string]*ProviderConfig{
			"openai": {
				API:    "chat",
				APIKey: "sk-base",
				Extra:  map[string]any{"user_agent": "base-client/1.0"},
			},
		},
	}
	override := Settings{
		Providers: map[string]*ProviderConfig{
			"openai": {
				API:   "responses",
				Extra: map[string]any{"user_agent": "override-client/1.0"},
			},
		},
	}

	merged := mergeSettings(base, override)
	pc := merged.Providers["openai"]
	if pc.API != "responses" {
		t.Fatalf("API = %q, want responses", pc.API)
	}
	if pc.APIKey != "sk-base" {
		t.Fatalf("APIKey = %q, want inherited key", pc.APIKey)
	}
	if got := pc.Extra["user_agent"]; got != "override-client/1.0" {
		t.Fatalf("Extra[user_agent] = %#v, want override-client/1.0", got)
	}
}

func TestProviderExtraIncludesAPI(t *testing.T) {
	pc := ProviderConfig{
		API:   "responses",
		Extra: map[string]any{"user_agent": "codebot-test/1.0"},
	}
	extra := pc.ProviderExtra()
	if extra["api"] != "responses" {
		t.Fatalf("extra[api] = %#v, want responses", extra["api"])
	}
	if extra["user_agent"] != "codebot-test/1.0" {
		t.Fatalf("extra[user_agent] = %#v, want codebot-test/1.0", extra["user_agent"])
	}
	if _, ok := pc.Extra["api"]; ok {
		t.Fatalf("ProviderExtra mutated original Extra: %#v", pc.Extra)
	}
}

func TestValidateResolvedRejectsInvalidProviderAPI(t *testing.T) {
	err := ValidateResolved(Resolved{
		Providers: map[string]ProviderConfig{
			"openai": {API: "legacy"},
		},
	})
	if err == nil {
		t.Fatal("invalid provider api should fail")
	}
	if !errors.Is(err, diag.ErrConfig) {
		t.Fatalf("expected diag.ErrConfig in chain, got %v", err)
	}
}

func TestValidateResolvedRejectsProviderAPIOnNonOpenAIProvider(t *testing.T) {
	err := ValidateResolved(Resolved{
		Providers: map[string]ProviderConfig{
			"anthropic": {API: "responses"},
		},
	})
	if err == nil {
		t.Fatal("non-OpenAI provider api should fail")
	}
	if !errors.Is(err, diag.ErrConfig) {
		t.Fatalf("expected diag.ErrConfig in chain, got %v", err)
	}
}
