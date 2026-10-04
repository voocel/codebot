package config

import (
	"context"
	"testing"
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
	conn := pc.connection()
	if conn.API != "responses" || conn.APIKey != "key" || conn.UserAgent != "codebot-test/1.0" {
		t.Fatalf("connection = %+v", conn)
	}
	// The explicit header wins over the alias.
	if len(conn.Headers) != 1 || conn.Headers["Anthropic-Beta"] != "explicit" {
		t.Fatalf("headers = %v", conn.Headers)
	}

	pc.Extra = &ProviderExtra{AnthropicBeta: "alias", Headers: headers}
	delete(headers, "Anthropic-Beta")
	if conn := pc.connection(); conn.Headers["anthropic-beta"] != "alias" || len(headers) != 0 {
		t.Fatalf("headers = %v; settings headers = %v", conn.Headers, headers)
	}
}

func TestConnectionBedrock(t *testing.T) {
	pc := ProviderConfig{Extra: &ProviderExtra{Region: "eu-west-1", AccessKeyID: "AKID", SecretAccessKey: "secret"}}
	if !pc.HasCredentials() {
		t.Fatal("AWS keys should count as credentials")
	}
	conn := pc.connection()
	creds, err := conn.Credentials.Credentials(context.Background())
	if err != nil || conn.Region != "eu-west-1" || creds.AccessKeyID != "AKID" || creds.SecretAccessKey != "secret" {
		t.Fatalf("connection = %+v, credentials = %+v, %v", conn, creds, err)
	}
	if (ProviderConfig{}).HasCredentials() {
		t.Fatal("empty provider has no credentials")
	}
}

func TestValidateResolvedRejectsInvalidProviderAPI(t *testing.T) {
	err := validateResolved(Resolved{
		Providers: map[string]ProviderConfig{
			"openai": {API: "legacy"},
		},
	})
	if err == nil {
		t.Fatal("invalid provider api should fail")
	}
}

func TestValidateResolvedRejectsProviderAPIOnNonOpenAIProvider(t *testing.T) {
	err := validateResolved(Resolved{
		Providers: map[string]ProviderConfig{
			"anthropic": {API: "responses"},
		},
	})
	if err == nil {
		t.Fatal("non-OpenAI provider api should fail")
	}
}

func TestValidateResolvedPromptCacheTTL(t *testing.T) {
	for ttl, ok := range map[string]bool{"": true, "5m": true, "1h": true, "24h": false} {
		if err := validateResolved(Resolved{PromptCacheTTL: ttl}); (err == nil) != ok {
			t.Errorf("prompt_cache_ttl %q: %v", ttl, err)
		}
	}
}
