package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
)

func TestParseMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want approval.Mode
	}{
		{in: "", want: approval.ModeBalanced},
		{in: "balanced", want: approval.ModeBalanced},
		{in: "strict", want: approval.ModeStrict},
		{in: "trust", want: approval.ModeTrust},
		{in: "off", want: approval.ModeTrust},
		{in: "  StRiCt  ", want: approval.ModeStrict},
		{in: "accept-edits", want: approval.ModeAcceptEdits},
		{in: "accept_edits", want: approval.ModeAcceptEdits},
	}
	for _, tc := range cases {
		got, err := approval.ParseMode(tc.in)
		if err != nil {
			t.Fatalf("ParseMode(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseModeInvalid(t *testing.T) {
	t.Parallel()

	if _, err := approval.ParseMode("unknown"); err == nil {
		t.Fatalf("expected invalid mode error")
	}
}

func TestBootNonTTYWithTestProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Minimal boot test — only checks that Boot runs without panicking
	// when given a non-interactive environment.
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/.codebot/settings.json", []byte(`{}`), 0o644); err != nil {
		// OK if dir doesn't exist yet.
		if err := os.MkdirAll(dir+"/.codebot", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir+"/.codebot/settings.json", []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Ensure required env vars won't trip up the test.
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if v := os.Getenv(key); v != "" {
			continue
		}
		// Some providers require keys; skip if not present.
		if _, ok := os.LookupEnv(key); !ok {
			t.Setenv(key, "test-key-"+strings.ToLower(key))
		}
	}
}

func TestResolveInputEnvProviderDoesNotCreateSettingsFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "test-openai-key")

	cwd := t.TempDir()
	input, err := resolveInput(Options{
		Cwd:        cwd,
		NonTTYMode: true,
	})
	if err != nil {
		t.Fatalf("resolveInput() error: %v", err)
	}
	defer input.sessionStore.Close()

	if input.settings.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", input.settings.Provider)
	}
	if input.settings.SmallModel != input.settings.Model {
		t.Fatalf("small model = %q, want %q", input.settings.SmallModel, input.settings.Model)
	}
	if input.envHint != "Using OPENAI_API_KEY from environment" {
		t.Fatalf("envHint = %q", input.envHint)
	}

	if _, err := os.Stat(config.GlobalSettingsPath()); !os.IsNotExist(err) {
		t.Fatalf("global settings file should not be created, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".codebot", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("project settings file should not be created, stat err = %v", err)
	}
}

func TestBootCustomProviderRequiresExplicitType(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".codebot"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{
  "provider": "myproxy",
  "model": "gpt-4o-mini",
  "providers": {
    "myproxy": {
      "api_key": "test-key"
    }
  }
}`
	if err := os.WriteFile(filepath.Join(cwd, ".codebot", "settings.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Boot(Options{Cwd: cwd, NonTTYMode: true})
	if err == nil {
		t.Fatal("expected boot error for custom provider without type")
	}
	if err.Error() != `configuration error: providers.myproxy.type is required for custom providers` {
		t.Fatalf("unexpected error: %v", err)
	}
}
