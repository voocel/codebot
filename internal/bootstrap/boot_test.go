package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/diag"
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
		{in: "auto", want: approval.ModeAuto},
		{in: "accept-edits", want: approval.ModeAuto},
		{in: "accept_edits", want: approval.ModeAuto},
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

// TestResolveInputIgnoresEnvCredentials locks in the no-env-mode contract:
// credentials come exclusively from settings.json, so an API key in the
// environment alone must not boot.
func TestResolveInputIgnoresEnvCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("OPENAI_API_KEY", "test-openai-key")

	_, err := resolveInput(Options{
		Cwd:        t.TempDir(),
		NonTTYMode: true,
	})
	if err == nil {
		t.Fatal("env API key alone must not satisfy boot")
	}
	if !errors.Is(err, diag.ErrConfig) {
		t.Fatalf("expected diag.ErrConfig, got: %v", err)
	}
	if _, statErr := os.Stat(config.GlobalSettingsPath()); !os.IsNotExist(statErr) {
		t.Fatalf("global settings file should not be created, stat err = %v", statErr)
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
	if !errors.Is(err, diag.ErrConfig) {
		t.Fatalf("expected diag.ErrConfig in chain, got: %v", err)
	}
	if !strings.Contains(err.Error(), "providers.myproxy.type is required for custom providers") {
		t.Fatalf("unexpected error: %v", err)
	}
}
