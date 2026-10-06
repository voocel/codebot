package prompt

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSystemHoldsTheConventions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()

	system := System(cwd)
	for _, marker := range []string{
		"expert coding assistant",
		"## Doing tasks",
		"## Using tools",
		"## System reminders",
		"## Communication",
		"## Auto memory",
	} {
		if !strings.Contains(system, marker) {
			t.Errorf("system prompt missing %q", marker)
		}
	}
	// What changes while a conversation lasts is told in Parts.
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

// A project's AGENTS.md and SYSTEM.md leading outside it are not read: the
// user's files are not the project's to read into the prompt.
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
}

func TestPartWithNothingToTellSaysSo(t *testing.T) {
	if got := MCP("").Text(); got != "# MCP Server Instructions\n\nNone." {
		t.Fatalf("text = %q", got)
	}
	if got := DeferredTools(nil); got.Body != "" {
		t.Fatalf("body = %q", got.Body)
	}
	if got := DeferredTools([]string{"deploy", "rollback"}).Body; !strings.HasSuffix(got, "\n\ndeploy\nrollback") {
		t.Fatalf("body = %q", got)
	}
}

func TestProjectAndMemory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("project rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Project(cwd).Body; got != "project rule" {
		t.Fatalf("project = %q", got)
	}
	// MEMORY.md is promised to be in context, so an empty one says so.
	if got := Memory(cwd).Body; !strings.Contains(got, "currently empty") {
		t.Fatalf("memory = %q", got)
	}
}

func TestGitOutsideARepository(t *testing.T) {
	if got := Git(t.TempDir()); got.Body != "" {
		t.Fatalf("body = %q", got.Body)
	}
}
