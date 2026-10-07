package prompt

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/voocel/codebot/internal/infra/config"
)

// The system prompt is the cached prefix; anything that changes belongs in a
// Part.
func TestSystemHoldsNothingThatChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()

	system := System(cwd)
	for _, changing := range []string{cwd, time.Now().Format("2006-01-02")} {
		if strings.Contains(system, changing) {
			t.Errorf("system prompt holds %q", changing)
		}
	}
}

func TestSystemFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(cwd, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("APPEND_SYSTEM.md", "appended rule")
	if system := System(cwd); !strings.Contains(system, "## Doing tasks") || !strings.HasSuffix(system, "\n\nappended rule") {
		t.Fatalf("APPEND_SYSTEM.md not appended to the built-in prompt:\n%s", system)
	}
	write("SYSTEM.md", "custom system prompt")
	if system := System(cwd); system != "custom system prompt\n\nappended rule" {
		t.Fatalf("system = %q", system)
	}
}

// Symlinked AGENTS.md and SYSTEM.md that point outside the project are not
// read, so a repository can't pull the user's files into the prompt.
func TestProjectFilesStayInTheProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	secret := filepath.Join(home, ".git-credentials")
	if err := os.WriteFile(secret, []byte("https://me:SECRET@github.com"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", "SYSTEM.md"} {
		if err := os.Symlink(secret, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "guide.md"), []byte("project rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("guide.md", filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if got := System(root) + loadAgents(root); strings.Contains(got, "SECRET") || !strings.Contains(got, "project rules") {
		t.Errorf("prompt %q", got)
	}

	// From a nested repository, the outer project's files still stay inside
	// it, while ~/.codebot/AGENTS.md may point anywhere.
	sub := filepath.Join(root, "vendor", "sub")
	if err := os.MkdirAll(filepath.Join(sub, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dotfiles := filepath.Join(home, "dotfiles.md")
	if err := os.WriteFile(dotfiles, []byte("my rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.UserConfigDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfiles, filepath.Join(config.UserConfigDir(), "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if got := loadAgents(sub); strings.Contains(got, "SECRET") || !strings.Contains(got, "my rules") || !strings.Contains(got, "project rules") {
		t.Errorf("agents %q", got)
	}
}

// An empty part renders as "None." to retract its earlier content. An empty
// MEMORY.md gets a placeholder because the instructions promise it is there.
func TestPartWithNothingToTellSaysSo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := MCP("").Text(); got != "# MCP Server Instructions\n\nNone." {
		t.Fatalf("text = %q", got)
	}
	if got := Memory(t.TempDir()).Body; !strings.Contains(got, "currently empty") {
		t.Fatalf("memory = %q", got)
	}
}
