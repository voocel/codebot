package config

import (
	"strings"
	"testing"

	"github.com/voocel/codebot/internal/skill"
)

func TestBuildSystemBlockTexts(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{Name: "read", Description: "Read files"},
		{Name: "bash", Description: "Run commands"},
	}
	identity, instructions := BuildSystemBlockTexts("/tmp/ws", ContextFiles{}, tools)

	if !strings.Contains(identity, "/tmp/ws") {
		t.Error("identity should contain working directory")
	}
	if !strings.Contains(instructions, "**read**") {
		t.Error("instructions should list read tool")
	}
	if !strings.Contains(instructions, "**bash**") {
		t.Error("instructions should list bash tool")
	}
	if strings.Contains(instructions, "**write**") {
		t.Error("instructions should not list tools not in the input")
	}
}

func TestBuildSystemBlockTextsNoTools(t *testing.T) {
	t.Parallel()

	identity, instructions := BuildSystemBlockTexts("/tmp/ws", ContextFiles{}, nil)

	if !strings.Contains(identity, "/tmp/ws") {
		t.Error("identity should contain working directory")
	}
	if strings.Contains(instructions, "## Tools") {
		t.Error("instructions should not contain tools section when no tools provided")
	}
}

func TestBuildSystemBlockTextsIncludesDoingTasksGuardrails(t *testing.T) {
	t.Parallel()

	_, instructions := BuildSystemBlockTexts("/tmp/ws", ContextFiles{}, []ToolInfo{{Name: "read"}})

	for _, marker := range []string{
		"## Doing tasks",
		`"improvements" beyond what was asked`,
		"scenarios that can't happen",
		"premature abstraction",
		"diagnose why before switching tactics",
		"OWASP top 10",
		"backwards-compatibility hacks",
		"## Using your tools",
		"Maximize use of parallel tool calls",
		"## Output efficiency",
		"Go straight to the point",
	} {
		if !strings.Contains(instructions, marker) {
			t.Errorf("instructions missing guardrail %q", marker)
		}
	}
}

func TestBuildSystemBlockTextsAddsTaskManagementSection(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{Name: "task_create", Description: "Create task"},
		{Name: "task_update", Description: "Update task"},
		{Name: "task_list", Description: "List tasks"},
	}

	_, instructions := BuildSystemBlockTexts("/tmp/ws", ContextFiles{}, tools)
	if !strings.Contains(instructions, "## Task Management") {
		t.Fatalf("expected task management section, got %q", instructions)
	}
	for _, name := range []string{"task_create", "task_update", "task_list"} {
		if !strings.Contains(instructions, name) {
			t.Fatalf("expected task management section to mention %s, got %q", name, instructions)
		}
	}
}

func TestBuildSystemBlockTextsSystemOverride(t *testing.T) {
	t.Parallel()

	ctx := ContextFiles{SystemOverride: "custom system prompt"}
	identity, instructions := BuildSystemBlockTexts("/tmp/ws", ctx, []ToolInfo{{Name: "read"}})

	if identity != "" {
		t.Error("identity should be empty when SystemOverride is set")
	}
	if instructions != "custom system prompt" {
		t.Errorf("instructions should be the override, got %q", instructions)
	}
}

func TestBuildReminders(t *testing.T) {
	t.Parallel()

	skills := []skill.Spec{
		{Name: "commit", Description: "Git commit", FilePath: "/skills/commit.md"},
	}
	ctx := ContextFiles{Agents: "project context here"}

	reminders := BuildReminders(ctx, skills)

	if len(reminders) < 2 {
		t.Fatalf("expected at least 2 reminders, got %d", len(reminders))
	}

	hasSkill := false
	hasContext := false
	for _, r := range reminders {
		if strings.Contains(r, "## Skills") {
			hasSkill = true
		}
		if strings.Contains(r, "project context here") {
			hasContext = true
		}
		if !strings.Contains(r, "<system-reminder>") {
			t.Errorf("reminder missing <system-reminder> wrapper: %s", r[:50])
		}
	}
	if !hasSkill {
		t.Error("reminders should contain skills")
	}
	if !hasContext {
		t.Error("reminders should contain project context")
	}
}

func TestBuildRemindersEmpty(t *testing.T) {
	t.Parallel()

	reminders := BuildReminders(ContextFiles{}, nil)
	if len(reminders) != 0 {
		t.Errorf("expected no reminders, got %d", len(reminders))
	}
}
