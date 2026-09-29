package config

import (
	"context"
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
				Extra:  &ProviderExtra{UserAgent: "base-client/1.0"},
			},
		},
	}
	override := Settings{
		Providers: map[string]*ProviderConfig{
			"openai": {
				API:   "responses",
				Extra: &ProviderExtra{UserAgent: "override-client/1.0"},
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
	if got := pc.Extra.UserAgent; got != "override-client/1.0" {
		t.Fatalf("Extra.UserAgent = %q, want override-client/1.0", got)
	}
}

func TestResolveDreamDefaults(t *testing.T) {
	r := Settings{}.Resolve()
	want := DreamSettings{Enabled: true, MinHours: 24, MinSessions: 5}
	if r.Dream != want {
		t.Fatalf("Dream = %+v, want %+v", r.Dream, want)
	}
}

func TestResolveDreamOverridesAndInvalidValues(t *testing.T) {
	off := false
	bad := -1
	hours := 48
	r := Settings{Dream: &DreamConfig{Enabled: &off, MinHours: &hours, MinSessions: &bad}}.Resolve()
	want := DreamSettings{Enabled: false, MinHours: 48, MinSessions: 5}
	if r.Dream != want {
		t.Fatalf("Dream = %+v, want %+v", r.Dream, want)
	}
}

func TestMergeSettingsDreamReplacesWhole(t *testing.T) {
	on := true
	off := false
	base := Settings{Dream: &DreamConfig{Enabled: &on}}
	override := Settings{Dream: &DreamConfig{Enabled: &off}}
	merged := mergeSettings(base, override)
	if merged.Dream.Enabled == nil || *merged.Dream.Enabled {
		t.Fatalf("Dream.Enabled = %v, want false (project overrides global)", merged.Dream.Enabled)
	}
	// No override → base kept.
	merged = mergeSettings(base, Settings{})
	if merged.Dream.Enabled == nil || !*merged.Dream.Enabled {
		t.Fatalf("Dream.Enabled = %v, want true (base preserved)", merged.Dream.Enabled)
	}
}

func TestConnection(t *testing.T) {
	headers := map[string]string{"Anthropic-Beta": "explicit"}
	pc := ProviderConfig{
		API:    "responses",
		APIKey: "key",
		Extra: &ProviderExtra{
			UserAgent:     "codebot-test/1.0",
			Headers:       headers,
			AnthropicBeta: "alias",
		},
	}
	conn := pc.Connection()
	if conn.API != "responses" || conn.APIKey != "key" || conn.UserAgent != "codebot-test/1.0" {
		t.Fatalf("connection = %+v", conn)
	}
	// The explicit header wins over the alias.
	if len(conn.Headers) != 1 || conn.Headers["Anthropic-Beta"] != "explicit" {
		t.Fatalf("headers = %v", conn.Headers)
	}

	pc.Extra = &ProviderExtra{AnthropicBeta: "alias", Headers: headers}
	delete(headers, "Anthropic-Beta")
	if conn := pc.Connection(); conn.Headers["anthropic-beta"] != "alias" || len(headers) != 0 {
		t.Fatalf("headers = %v; settings headers = %v", conn.Headers, headers)
	}
}

func TestConnectionBedrock(t *testing.T) {
	pc := ProviderConfig{Extra: &ProviderExtra{Region: "eu-west-1", AccessKeyID: "AKID", SecretAccessKey: "secret"}}
	if !pc.HasCredentials() {
		t.Fatal("AWS keys should count as credentials")
	}
	conn := pc.Connection()
	creds, err := conn.Credentials.Credentials(context.Background())
	if err != nil || conn.Region != "eu-west-1" || creds.AccessKeyID != "AKID" || creds.SecretAccessKey != "secret" {
		t.Fatalf("connection = %+v, credentials = %+v, %v", conn, creds, err)
	}
	if (ProviderConfig{}).HasCredentials() {
		t.Fatal("empty provider has no credentials")
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

// Model-derived reserves remain bounded and allow explicit overrides.
func TestCompactReserveTracksModelOutputCeiling(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		r    Resolved
		want int
	}{
		{"small ceiling reserves only what it needs", Resolved{ContextWindow: 200_000, MaxOutputTokens: 8_192}, 8_192},
		{"large ceiling hits the cap", Resolved{ContextWindow: 1_000_000, MaxOutputTokens: 128_000}, 20_000},
		{"capped window clamps the reserve", Resolved{ContextWindow: 20_000, MaxOutputTokens: 64_000}, 10_000},
		{"unknown ceiling defers to the engine", Resolved{ContextWindow: 128_000}, 0},
		{"explicit ratio wins", Resolved{ContextWindow: 100_000, MaxOutputTokens: 64_000, CompactRatio: 0.8}, 20_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.CompactReserveTokens(); got != tc.want {
				t.Fatalf("CompactReserveTokens() = %d, want %d", got, tc.want)
			}
		})
	}
}
