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
		"## Parallel tool execution (CRITICAL)",
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

// Team coordination section is only useful when ALL four team-related tools
// are present — partial sets (e.g. just team_create without send_message)
// would describe a workflow the LLM cannot actually execute, and that's
// worse than no documentation.
func TestBuildSystemBlockTextsAddsTeamSectionOnlyWithFullToolset(t *testing.T) {
	t.Parallel()

	full := []ToolInfo{
		{Name: "team_create"},
		{Name: "team_dismiss"},
		{Name: "send_message"},
		{Name: "subagent"},
	}
	_, withFull := BuildSystemBlockTexts("/tmp/ws", ContextFiles{}, full)
	if !strings.Contains(withFull, "## Team coordination") {
		t.Fatal("expected team coordination section when all four tools present")
	}
	// Spot-check critical workflow phrases so trivial wording changes don't
	// silently delete the section the LLM relies on.
	for _, marker := range []string{
		"ONE team per session",
		"team_create",
		"team_dismiss",
		"send_message",
		"team_name",
		`<teammate-message`,
	} {
		if !strings.Contains(withFull, marker) {
			t.Errorf("team section missing marker %q", marker)
		}
	}

	// Drop one of the four — section must disappear.
	partial := []ToolInfo{
		{Name: "team_create"},
		{Name: "send_message"},
		{Name: "subagent"},
		// no team_dismiss
	}
	_, withoutDismiss := BuildSystemBlockTexts("/tmp/ws", ContextFiles{}, partial)
	if strings.Contains(withoutDismiss, "## Team coordination") {
		t.Error("team coordination section should NOT appear when team_dismiss is absent")
	}
}

func TestBuildSystemBlockTextsOmitsTeamSectionWithoutTeamTools(t *testing.T) {
	t.Parallel()

	_, instructions := BuildSystemBlockTexts("/tmp/ws", ContextFiles{}, []ToolInfo{{Name: "read"}})
	if strings.Contains(instructions, "## Team coordination") {
		t.Error("team coordination section leaked into prompt without team tools")
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

func TestSplitToolsByOrigin(t *testing.T) {
	t.Parallel()

	local, mcp := SplitToolsByOrigin([]ToolInfo{
		{Name: "read", Description: "Read"},
		{Name: "mcp__docs__search", Description: "Search docs"},
		{Name: "bash", Description: "Run shell"},
		{Name: "mcp__ops__deploy", Description: "Deploy"},
	})

	if got := names(local); !equal(got, []string{"read", "bash"}) {
		t.Fatalf("local tools = %v, want [read bash]", got)
	}
	if got := names(mcp); !equal(got, []string{"mcp__docs__search", "mcp__ops__deploy"}) {
		t.Fatalf("mcp tools = %v, want [mcp__docs__search mcp__ops__deploy]", got)
	}
}

func TestBuildFrozenSystemPartsDoesNotIncludeMCP(t *testing.T) {
	t.Parallel()

	// MCP tools must be filtered out by callers — BuildFrozenSystemParts
	// includes whatever is handed to it. This test verifies that callers
	// passing pure local tools do not accidentally see an MCP entry.
	_, frozen := BuildFrozenSystemParts("/tmp/ws", ContextFiles{}, []ToolInfo{
		{Name: "read", Description: "Read"},
	})
	if strings.Contains(frozen, "mcp__") {
		t.Fatalf("frozen instructions should not mention mcp tools when none are supplied: %q", frozen)
	}
}

func TestBuildDynamicSystemPartEmpty(t *testing.T) {
	t.Parallel()

	if got := BuildDynamicSystemPart(nil, nil); got != "" {
		t.Fatalf("empty inputs must produce empty string, got %q", got)
	}
}

func TestBuildDynamicSystemPartCombinesToolsAndOverlays(t *testing.T) {
	t.Parallel()

	got := BuildDynamicSystemPart(
		[]ToolInfo{{Name: "mcp__docs__search", Description: "Search"}},
		[]string{"plan mode overlay", "mcp server instructions"},
	)
	for _, want := range []string{"## MCP Tools", "**mcp__docs__search**", "plan mode overlay", "mcp server instructions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dynamic part missing %q: %q", want, got)
		}
	}
}

func TestBuildDynamicSystemPartStableForSameInputs(t *testing.T) {
	t.Parallel()

	// The dynamic block hashes into a cache fingerprint — same inputs must
	// produce byte-identical output across calls.
	tools := []ToolInfo{{Name: "mcp__a__one", Description: "A"}, {Name: "mcp__b__two", Description: "B"}}
	overlays := []string{"x", "y"}
	first := BuildDynamicSystemPart(tools, overlays)
	second := BuildDynamicSystemPart(tools, overlays)
	if first != second {
		t.Fatal("identical inputs must yield identical output")
	}
}

func TestBuildSystemBlockTextsRoutesMCPThroughDynamic(t *testing.T) {
	t.Parallel()

	// Backwards-compat wrapper: MCP tools handed in via the legacy single-
	// argument API must end up in the dynamic ## MCP Tools section, not the
	// frozen ## Tools section.
	_, instructions := BuildSystemBlockTexts("/tmp/ws", ContextFiles{}, []ToolInfo{
		{Name: "read", Description: "Read"},
		{Name: "mcp__docs__search", Description: "Search docs"},
	})
	if !strings.Contains(instructions, "## Tools") {
		t.Fatal("local tools section missing")
	}
	if !strings.Contains(instructions, "## MCP Tools") {
		t.Fatal("MCP tools should be promoted to the dynamic section, even via the wrapper")
	}
	// Local section comes before MCP section.
	if strings.Index(instructions, "## Tools") > strings.Index(instructions, "## MCP Tools") {
		t.Fatal("local tools section should precede MCP tools section")
	}
}

func names(infos []ToolInfo) []string {
	out := make([]string, len(infos))
	for i, t := range infos {
		out[i] = t.Name
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBuildRemindersEmpty(t *testing.T) {
	t.Parallel()

	// Date is always surfaced as a reminder for cache-stable system prompts;
	// an otherwise-empty context should yield exactly the date reminder.
	reminders := BuildReminders(ContextFiles{}, nil)
	if len(reminders) != 1 {
		t.Fatalf("expected 1 reminder (date only), got %d", len(reminders))
	}
	if !strings.Contains(reminders[0], "Today's date is") {
		t.Errorf("expected date reminder, got %q", reminders[0])
	}
}
