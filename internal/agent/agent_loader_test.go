package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAgentFile is a test helper: drops a markdown file into dir with the
// given content, returns the full path. Errors are fatal — there is no
// graceful path for a setup failure.
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

// Happy path: a well-formed file loads cleanly and every field round-trips.
func TestLoadAgent_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "reviewer.md", `---
name: code-reviewer
description: Independent code reviewer agent.
tools: [read, grep, glob]
disallowedTools: [bash]
model: inherit
maxTurns: 25
background: true
---

You are a code reviewer.

Look for null pointer risks and unhandled errors.
`)

	defs, errs := LoadAgentsDir(dir, SourceProject)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}
	d := defs[0]
	if d.Name != "code-reviewer" {
		t.Errorf("Name = %q", d.Name)
	}
	if d.Description != "Independent code reviewer agent." {
		t.Errorf("Description = %q", d.Description)
	}
	if !strings.Contains(d.SystemPrompt, "You are a code reviewer.") {
		t.Errorf("SystemPrompt missing body, got %q", d.SystemPrompt)
	}
	if strings.HasPrefix(d.SystemPrompt, "\n") {
		t.Errorf("SystemPrompt should be left-trimmed, got %q", d.SystemPrompt[:10])
	}
	if got := d.Tools; len(got) != 3 || got[0] != "read" {
		t.Errorf("Tools = %v", got)
	}
	if got := d.DisallowedTools; len(got) != 1 || got[0] != "bash" {
		t.Errorf("DisallowedTools = %v", got)
	}
	if d.Model != "inherit" {
		t.Errorf("Model = %q", d.Model)
	}
	if d.MaxTurns != 25 {
		t.Errorf("MaxTurns = %d", d.MaxTurns)
	}
	if !d.Background {
		t.Error("Background should be true")
	}
	if d.Source != SourceProject {
		t.Errorf("Source = %q", d.Source)
	}
	if d.Filename != "reviewer.md" {
		t.Errorf("Filename = %q", d.Filename)
	}
}

// Filename fallback: when frontmatter omits `name`, the loader infers it
// from the filename stem. This is a deliberate ergonomic shortcut for
// single-purpose files.
func TestLoadAgent_NameDefaultsToFilename(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "summariser.md", `---
description: Summarises long files.
---
You summarise.
`)

	defs, errs := LoadAgentsDir(dir, SourceUser)
	if len(errs) != 0 || len(defs) != 1 {
		t.Fatalf("load failed: errs=%v defs=%d", errs, len(defs))
	}
	if defs[0].Name != "summariser" {
		t.Errorf("Name fallback failed, got %q", defs[0].Name)
	}
}

// Strict schema: an unknown key (typo `tooLs:` instead of `tools:`) must
// produce a load error, not silently load an agent without the intended
// tools list. This is the whole point of KnownFields(true).
func TestLoadAgent_UnknownFieldFails(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "typo.md", `---
name: typo
description: has a typo
tooLs: [read]
---
Body.
`)

	defs, errs := LoadAgentsDir(dir, SourceProject)
	if len(defs) != 0 {
		t.Errorf("definition should not have loaded, got %v", defs)
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "tooLs") {
		t.Errorf("error should mention the unknown field, got %v", errs[0])
	}
}

// Missing frontmatter is an error: every agent must declare its metadata
// explicitly. We never infer everything from filename + body because that
// would create files that "work" by accident and fail mysteriously later.
func TestLoadAgent_MissingFrontmatterFails(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "naked.md", `Just a body, no frontmatter.
`)

	_, errs := LoadAgentsDir(dir, SourceProject)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
	if !strings.Contains(errs[0].Error(), "frontmatter") {
		t.Errorf("error should mention frontmatter, got %v", errs[0])
	}
}

// Unclosed frontmatter is also an error — guards against an editor that
// stripped the closing delimiter or a copy-paste mistake.
func TestLoadAgent_UnclosedFrontmatterFails(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "unclosed.md", `---
name: oops
description: never closes
body without delimiter
`)

	_, errs := LoadAgentsDir(dir, SourceProject)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
	if !strings.Contains(errs[0].Error(), "not closed") {
		t.Errorf("error should mention closure, got %v", errs[0])
	}
}

// Empty body is an error — Validate() catches it. An agent with no system
// prompt is meaningless and almost certainly a user mistake.
func TestLoadAgent_EmptyBodyFails(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "empty.md", `---
name: empty
description: has no body
---
`)

	_, errs := LoadAgentsDir(dir, SourceProject)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
	if !strings.Contains(errs[0].Error(), "system prompt") {
		t.Errorf("error should mention system prompt, got %v", errs[0])
	}
}

// One bad file shouldn't poison the directory: good files alongside a bad
// one still load successfully. This is critical for the "iterate on a
// custom agent" workflow — the user should still have their other agents.
func TestLoadAgent_PartialFailureIsolated(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "good.md", `---
name: good
description: works fine
---
Body.
`)
	writeAgentFile(t, dir, "bad.md", `---
unclosed
`)

	defs, errs := LoadAgentsDir(dir, SourceProject)
	if len(defs) != 1 || defs[0].Name != "good" {
		t.Errorf("good agent should have loaded, got %v", defs)
	}
	if len(errs) != 1 {
		t.Errorf("expected exactly 1 error from bad.md, got %d: %v", len(errs), errs)
	}
}

// Missing directory is silent success: the loader is happy to be called
// against a directory that hasn't been created yet. Bootstrap code can
// speculatively call LoadAgentsDir(.codebot/agents/) without checking
// existence first.
func TestLoadAgent_MissingDirIsOK(t *testing.T) {
	defs, errs := LoadAgentsDir("/tmp/definitely-not-real-codebot-agents-dir-xyz123", SourceProject)
	if defs != nil {
		t.Errorf("expected nil defs, got %v", defs)
	}
	if errs != nil {
		t.Errorf("expected nil errs, got %v", errs)
	}
}

// Non-markdown files are ignored. A README.md in the agents dir would be
// rejected (it has no frontmatter), but a README.txt should be skipped
// entirely so users can document their agent library inline.
func TestLoadAgent_IgnoresNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "README.txt", "not an agent\n")
	writeAgentFile(t, dir, "good.md", `---
name: good
description: works
---
Body.
`)

	defs, errs := LoadAgentsDir(dir, SourceProject)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(defs) != 1 || defs[0].Name != "good" {
		t.Errorf("expected just the .md file to load, got %v", defs)
	}
}
