package config

import (
	"strings"
	"testing"
)

func TestBuildGuidelinesFullToolSet(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{Name: "read"}, {Name: "write"}, {Name: "edit"},
		{Name: "bash"}, {Name: "find"}, {Name: "grep"}, {Name: "ls"},
	}
	g := buildGuidelines(tools)

	expect := []string{
		"Read files before modifying them",
		"Use find/grep to explore",
		"Use edit for targeted changes",
		"Don't over-engineer",
		"prefer absolute paths",
		"ask for clarification",
	}
	for _, s := range expect {
		if !strings.Contains(g, s) {
			t.Errorf("full tool set guidelines missing: %q", s)
		}
	}
}

func TestBuildGuidelinesReadOnlyToolSet(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{Name: "read"}, {Name: "find"}, {Name: "grep"}, {Name: "ls"},
	}
	g := buildGuidelines(tools)

	if strings.Contains(g, "Read files before modifying them") {
		t.Error("read-only set should not contain modification guideline")
	}
	if strings.Contains(g, "Use edit for targeted changes") {
		t.Error("read-only set should not contain edit guideline")
	}
	if strings.Contains(g, "prefer absolute paths") {
		t.Error("read-only set without bash should not contain bash guideline")
	}
	if !strings.Contains(g, "Use find/grep to explore") {
		t.Error("read-only set should contain find/grep guideline")
	}
	if !strings.Contains(g, "Don't over-engineer") {
		t.Error("read-only set should contain always-present guidelines")
	}
}

func TestBuildSystemPromptDynamicTools(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{Name: "read", Description: "Read files"},
		{Name: "bash", Description: "Run commands"},
	}
	prompt := BuildSystemPrompt("/tmp/ws", ContextFiles{}, tools)

	if !strings.Contains(prompt, "**read**") {
		t.Error("prompt should list read tool")
	}
	if !strings.Contains(prompt, "**bash**") {
		t.Error("prompt should list bash tool")
	}
	if strings.Contains(prompt, "**write**") {
		t.Error("prompt should not list tools not in the input")
	}
	// Dynamic guidelines: no write/edit means no "edit for targeted changes"
	if strings.Contains(prompt, "Use edit for targeted changes") {
		t.Error("prompt should not contain edit guideline without edit tool")
	}
}

func TestBuildSystemPromptStaticFallback(t *testing.T) {
	t.Parallel()

	prompt := BuildSystemPrompt("/tmp/ws", ContextFiles{}, nil)

	// Static fallback should contain all tools
	for _, tool := range []string{"read", "write", "edit", "bash", "find", "grep", "ls"} {
		if !strings.Contains(prompt, "**"+tool+"**") {
			t.Errorf("static fallback should list %s tool", tool)
		}
	}
	// Static guidelines
	if !strings.Contains(prompt, "Use edit for targeted changes") {
		t.Error("static fallback should contain all guidelines")
	}
}
