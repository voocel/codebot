package prompt

import (
	"strings"
	"testing"
)

func buildBlocksForTest(cwd string, ctx ContextFiles) (identity, instructions string) {
	return Identity(cwd, ctx), Instructions(ctx, "")
}

func TestBuildBlocksIncludesDoingTasksGuardrails(t *testing.T) {
	t.Parallel()

	// These guardrails live in the identity block.
	identity, _ := buildBlocksForTest("/tmp/ws", ContextFiles{})

	for _, marker := range []string{
		"## Doing tasks",
		"## Using tools",
		"## System reminders",
		"## Communication",
	} {
		if !strings.Contains(identity, marker) {
			t.Errorf("identity block missing section %q", marker)
		}
	}
}

func TestBuildBlocksSystemOverride(t *testing.T) {
	t.Parallel()

	ctx := ContextFiles{SystemOverride: "custom system prompt"}
	identity, instructions := buildBlocksForTest("/tmp/ws", ctx)

	if identity != "" {
		t.Error("identity should be empty when SystemOverride is set")
	}
	if instructions != "custom system prompt" {
		t.Errorf("instructions should be the override, got %q", instructions)
	}
}

// Workspace context belongs in the cached block 2, never in a per-turn
// reminder — that is the whole point of the layout. See tasks/todo.md.
func TestFrozenBlockCarriesWorkspaceContext(t *testing.T) {
	t.Parallel()

	skills := "- commit: Git commit"
	ctx := ContextFiles{
		Agents:       "project context here",
		Memory:       "remembered fact",
		MemoryDir:    "/tmp/mem",
		SystemAppend: "appended rule",
	}

	frozen := Instructions(ctx, skills)

	for _, want := range []string{
		"## Skills", "commit",
		"## Project Context", "project context here",
		"## Memory", "remembered fact",
		"appended rule",
	} {
		if !strings.Contains(frozen, want) {
			t.Errorf("frozen block missing %q", want)
		}
	}
	if strings.Contains(frozen, "<system-reminder>") {
		t.Error("frozen block must not wrap content as a per-turn reminder")
	}
}

// Same cwd must render byte-identical block 1, or every rebuild pays a cache
// write for nothing.
func TestIdentityBlockIsByteStable(t *testing.T) {
	t.Parallel()

	first := buildIdentityBlock("/tmp/ws")
	if first != buildIdentityBlock("/tmp/ws") {
		t.Fatal("identity block must be byte-stable for the same input")
	}
	for _, want := range []string{"expert coding assistant", "/tmp/ws", "Today's date: "} {
		if !strings.Contains(first, want) {
			t.Errorf("identity block missing %q", want)
		}
	}
}
