package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestApplySetupKeepsTheRest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(UserSettingsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	before := `{"provider": "openai", "model": "gpt-x", "providers": {
		"openai": {"api_key": "sk-o", "models": ["gpt-x"]},
		"anthropic": {"api_key": "sk-old", "models": ["claude-a"]}}}`
	if err := os.WriteFile(UserSettingsPath(), []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, model := range []string{"claude-b", "claude-a"} {
		if err := ApplySetup(SetupChoice{Provider: "anthropic", APIKey: "sk-new", Model: model}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := loadFile(UserSettingsPath(), os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	if a := s.Providers["anthropic"]; *s.Model != "claude-a" || a.APIKey != "sk-new" || !slices.Equal(a.Models, []string{"claude-a", "claude-b"}) {
		t.Errorf("anthropic = %+v, model %s", a, *s.Model)
	}
	if o := s.Providers["openai"]; o.APIKey != "sk-o" || !slices.Equal(o.Models, []string{"gpt-x"}) {
		t.Errorf("openai = %+v", o)
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
}
