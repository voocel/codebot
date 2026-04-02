package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundledSpecsIncludedInCatalog(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog(t.TempDir())
	spec, ok := catalog.Get("review")
	if !ok {
		t.Fatal("expected bundled review skill to be present")
	}
	if spec.Source != "bundled" {
		t.Fatalf("expected bundled source, got %q", spec.Source)
	}
	if spec.GetPrompt == nil {
		t.Fatal("expected bundled skill prompt function")
	}
}

func TestBundledSkillPromptExpansion(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog(t.TempDir())
	result, err := ProcessInvocation(context.Background(), catalog, InvokeInput{
		Name:      "debug",
		Args:      "failing test",
		SessionID: "sess-1",
		Source:    SourceUser,
	})
	if err != nil {
		t.Fatalf("ProcessInvocation error: %v", err)
	}
	if !strings.Contains(result.PromptText, "failing test") {
		t.Fatalf("expected bundled prompt to include args, got %q", result.PromptText)
	}
	if !strings.Contains(result.PromptText, `<skill name="debug">`) {
		t.Fatalf("expected skill wrapper, got %q", result.PromptText)
	}
}

func TestProjectSkillOverridesBundledSkill(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	skillsDir := filepath.Join(cwd, ".codebot", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "review.md"), []byte(
		"---\nname: review\ndescription: Project review override\n---\nProject-specific review instructions.",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog := NewCatalog(cwd)
	spec, ok := catalog.Get("review")
	if !ok {
		t.Fatal("expected review skill")
	}
	if spec.Source != "project" {
		t.Fatalf("expected project override, got %q", spec.Source)
	}
	if spec.Description != "Project review override" {
		t.Fatalf("unexpected description: %q", spec.Description)
	}

	result, err := ProcessInvocation(context.Background(), catalog, InvokeInput{
		Name:      "review",
		SessionID: "sess-2",
		Source:    SourceUser,
	})
	if err != nil {
		t.Fatalf("ProcessInvocation error: %v", err)
	}
	if !strings.Contains(result.PromptText, "Project-specific review instructions.") {
		t.Fatalf("expected project prompt content, got %q", result.PromptText)
	}
}
