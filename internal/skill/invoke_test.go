package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrderForPromptPrefersApplicableAndUsedSkills(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "internal", "skill"), 0o755); err != nil {
		t.Fatal(err)
	}

	skills := []Spec{
		{Name: "unused", Source: "bundled"},
		{Name: "review", Source: "bundled", Paths: []string{"internal/skill/**"}},
		{Name: "debug", Source: "project"},
	}
	ordered := OrderForPrompt(skills, cwd, map[string]float64{
		"review": 3,
		"debug":  1,
	})

	if ordered[0].Name != "review" {
		t.Fatalf("expected applicable + most-used skill first, got %#v", ordered)
	}
	if ordered[1].Name != "debug" {
		t.Fatalf("expected next used/project skill second, got %#v", ordered)
	}
}

func TestUntrustedSkillSourceDisablesPrivilegedFields(t *testing.T) {
	t.Parallel()

	spec := Spec{
		Name:         "remote-review",
		Source:       "remote",
		BaseDir:      "/tmp/remote-skill",
		Model:        "gpt-5",
		Effort:       "high",
		Paths:        []string{"internal/skill/**"},
		AllowedTools: []string{"bash"},
		Hooks: HooksConfig{
			"Notification": {
				{Type: "command", Command: "echo hi"},
			},
		},
	}
	spec.GetPrompt = buildStaticPromptFn(spec, "result: !`echo 42`")

	result, err := ProcessInvocation(context.Background(), NewStaticCatalog([]Spec{spec}), InvokeInput{
		Name:      "remote-review",
		SessionID: "sess-1",
		Source:    SourceUser,
	})
	if err != nil {
		t.Fatalf("ProcessInvocation error: %v", err)
	}

	if len(result.Delta.AllowedTools) != 0 {
		t.Fatalf("expected allowed tools stripped for untrusted source, got %#v", result.Delta.AllowedTools)
	}
	if result.Delta.Hooks != nil {
		t.Fatalf("expected hooks stripped for untrusted source, got %#v", result.Delta.Hooks)
	}
	if result.Delta.ModelOverride != "" {
		t.Fatalf("expected model override stripped for untrusted source, got %q", result.Delta.ModelOverride)
	}
	if result.Delta.Effort != "" {
		t.Fatalf("expected effort stripped for untrusted source, got %q", result.Delta.Effort)
	}
	if result.Delta.Paths != nil {
		t.Fatalf("expected paths stripped for untrusted source, got %#v", result.Delta.Paths)
	}
	if !strings.Contains(result.PromptText, "!`echo 42`") {
		t.Fatalf("expected prompt text to remain literal for untrusted source, got %q", result.PromptText)
	}
}
