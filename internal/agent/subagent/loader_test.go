package subagent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeAgentFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestLoadAgent_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "reviewer.md", `---
name: code-reviewer
description: Independent code reviewer agent.
tools: [read, grep, glob]
disallowedTools: [bash]
model: inherit
maxTurns: 25
---

You are a code reviewer.

Look for null pointer risks and unhandled errors.
`)

	defs, errs := LoadDir(dir)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := []AgentDefinition{{
		Name:            "code-reviewer",
		Description:     "Independent code reviewer agent.",
		SystemPrompt:    "You are a code reviewer.\n\nLook for null pointer risks and unhandled errors.",
		Tools:           []string{"read", "grep", "glob"},
		DisallowedTools: []string{"bash"},
		Model:           "inherit",
		MaxTurns:        25,
		Origin:          filepath.Join(dir, "reviewer.md"),
	}}
	if !reflect.DeepEqual(defs, want) {
		t.Errorf("loaded %+v\nwant   %+v", defs, want)
	}
}

// Each broken file is reported with its own problem, and good files beside
// it still load.
func TestLoadAgent_PartialFailureIsolated(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "good.md", `---
name: good
description: works fine
---
Body.
`)
	bad := []struct{ name, content, want string }{ // by name, as LoadDir reads them
		{"empty.md", "---\nname: empty\ndescription: has no body\n---\n", "system prompt"},
		{"naked.md", "Just a body, no frontmatter.\n", "frontmatter"},
		{"typo.md", "---\nname: typo\ndescription: has a typo\ntooLs: [read]\n---\nBody.\n", "tooLs"},
		{"unclosed.md", "---\nunclosed\n", "frontmatter"},
	}
	for _, b := range bad {
		writeAgentFile(t, dir, b.name, b.content)
	}

	defs, errs := LoadDir(dir)
	if len(defs) != 1 || defs[0].Name != "good" {
		t.Errorf("good agent should have loaded, got %v", defs)
	}
	if len(errs) != len(bad) {
		t.Fatalf("expected one error per bad file, got %d: %v", len(errs), errs)
	}
	for i, b := range bad {
		if err := errs[i].Error(); !strings.Contains(err, b.name) || !strings.Contains(err, b.want) {
			t.Errorf("%s: error %q should mention %q", b.name, err, b.want)
		}
	}
}

// Most projects have no .codebot/agents/, so a missing dir is not an error.
func TestLoadAgent_MissingDirIsOK(t *testing.T) {
	defs, errs := LoadDir(filepath.Join(t.TempDir(), "agents"))
	if defs != nil {
		t.Errorf("expected nil defs, got %v", defs)
	}
	if errs != nil {
		t.Errorf("expected nil errs, got %v", errs)
	}
}

// A README.txt is skipped so users can document their agents. A file without
// `name` is named after its filename.
func TestLoadAgent_IgnoresNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "README.txt", "not an agent\n")
	writeAgentFile(t, dir, "good.md", `---
description: works
---
Body.
`)

	defs, errs := LoadDir(dir)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(defs) != 1 || defs[0].Name != "good" {
		t.Errorf("expected just the .md file to load, got %v", defs)
	}
}
