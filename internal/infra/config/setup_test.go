package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeUserSettings(t *testing.T, data string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(UserSettingsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserSettingsPath(), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApplySetupKeepsTheRest(t *testing.T) {
	writeUserSettings(t, `{"provider": "openai", "model": "gpt-x", "reasoning_effort": "high", "providers": {
		"openai": {"api_key": "sk-o", "models": ["gpt-x"]},
		"anthropic": {"api_key": "sk-old", "models": ["claude-a"]}}}`)

	for _, model := range []string{"claude-b", "claude-a"} {
		if err := ApplySetup(SetupChoice{Provider: "anthropic", APIKey: "sk-new", Model: model}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := UserSettings()
	if err != nil {
		t.Fatal(err)
	}
	if a := s.Providers["anthropic"]; *s.Model != "claude-a" || a.APIKey != "sk-new" || !slices.Equal(a.Models, []string{"claude-a", "claude-b"}) {
		t.Errorf("anthropic = %+v, model %s", a, *s.Model)
	}
	if o := s.Providers["openai"]; o.APIKey != "sk-o" || !slices.Equal(o.Models, []string{"gpt-x"}) {
		t.Errorf("openai = %+v", o)
	}
	// The effort picked for gpt-x may not suit claude-a.
	if s.ReasoningEffort != nil {
		t.Errorf("reasoning_effort = %q", *s.ReasoningEffort)
	}
}

// The setup saves the connection it checked: no key, URL or api of the
// endpoint's old description stays, but its headers and models do.
func TestApplySetupReplacesTheConnection(t *testing.T) {
	writeUserSettings(t, `{"providers": {"work": {"type": "openai", "api": "responses", "base_url": "https://a.example/v1",
		"api_key": "secret-a", "models": ["m1"], "extra": {"headers": {"X-Team": "core"}}}}}`)
	c := SetupChoice{Provider: "work", Type: "compat", BaseURL: "https://b.example/v1", Model: "m2"}
	if err := ApplySetup(c); err != nil {
		t.Fatal(err)
	}
	s, err := UserSettings()
	if err != nil {
		t.Fatal(err)
	}
	w := s.Providers["work"]
	if w.Type != "compat" || w.API != "" || w.BaseURL != "https://b.example/v1" || w.APIKey != "" {
		t.Errorf("connection = %+v", w)
	}
	if !slices.Equal(w.Models, []string{"m1", "m2"}) || w.Extra == nil || w.Extra.Headers["X-Team"] != "core" {
		t.Errorf("the rest = %+v", w)
	}
	if _, err := (Layers{User: s}).Resolve(t.TempDir(), Settings{}); err != nil {
		t.Errorf("the saved settings fail: %v", err)
	}
}

func TestApplySetupChecksTheProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := ApplySetup(SetupChoice{Provider: "ollama", Model: "llama3"}); err != nil {
		t.Errorf("ollama without a key: %v", err)
	}
	for name, c := range map[string]SetupChoice{
		"no key":      {Provider: "anthropic", Model: "claude-x"},
		"no base URL": {Provider: "local", Type: "compat", Model: "m"},
		"no protocol": {Provider: "local", BaseURL: "http://localhost:8080/v1", Model: "m"},
	} {
		if err := ApplySetup(c); err == nil {
			t.Errorf("%s: saved", name)
		}
	}
	// The error names the endpoint, not its protocol.
	if err := (SetupChoice{Provider: "local", Type: "anthropic", BaseURL: "https://a.example"}).Check(); err == nil || err.Error() != "local: api key is required" {
		t.Errorf("check = %v", err)
	}
}
