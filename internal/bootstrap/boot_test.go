package bootstrap

import (
	"os"
	"strings"
	"testing"

	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/storage"
)

func TestParseProfile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want approval.Profile
	}{
		{in: "", want: approval.ProfileBalanced},
		{in: "balanced", want: approval.ProfileBalanced},
		{in: "strict", want: approval.ProfileStrict},
		{in: "off", want: approval.ProfileOff},
		{in: "  StRiCt  ", want: approval.ProfileStrict},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := approval.ParseProfile(tc.in)
			if err != nil {
				t.Fatalf("parseProfile(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parseProfile(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseProfileInvalid(t *testing.T) {
	t.Parallel()

	if _, err := approval.ParseProfile("unknown"); err == nil {
		t.Fatalf("expected invalid profile error")
	}
}

func TestResolveSessionResumeRejectsNonTTY(t *testing.T) {
	mgr := storage.NewManager(t.TempDir())
	store, err := mgr.Create(t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}

	_, err = resolveSession(mgr, t.TempDir(), false, true, true)
	if err == nil {
		t.Fatalf("expected non-tty resume error")
	}
	if !strings.Contains(err.Error(), "-r requires interactive terminal") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveSessionResumeByIDSupportsEOFInput(t *testing.T) {
	mgr := storage.NewManager(t.TempDir())
	store, err := mgr.Create(t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	wantID := store.Header().SessionID
	if err := store.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := writer.WriteString(wantID); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	oldStdin := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = reader.Close()
	})

	opened, err := resolveSession(mgr, t.TempDir(), false, true, false)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	t.Cleanup(func() {
		_ = opened.Close()
	})
	if got := opened.Header().SessionID; got != wantID {
		t.Fatalf("resolved session id = %s, want %s", got, wantID)
	}
}
