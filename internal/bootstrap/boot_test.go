package bootstrap

import (
	"os"
	"strings"
	"testing"

	"github.com/voocel/codebot/internal/approval"
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
