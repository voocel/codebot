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

	// After the universal-base / role-block split these guardrails live in
	// the agent-agnostic identity block (so teammates inherit them too), not
	// the leader-only instructions block.
	identity, _ := BuildSystemBlockTexts("/tmp/ws", ContextFiles{}, []ToolInfo{{Name: "read"}})

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
		if !strings.Contains(identity, marker) {
			t.Errorf("identity (universal base) missing guardrail %q", marker)
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

// TestBuildUniversalBase_ByteStable guards the precondition for cross-agent
// prompt cache reuse: same cwd → byte-identical output, every call. Any
// future drift here (e.g. inserting a time.Now() or randomized ordering)
// silently destroys teammate cache hits, so the test is intentionally strict.
func TestBuildUniversalBase_ByteStable(t *testing.T) {
	t.Parallel()

	first := BuildUniversalBase("/tmp/ws")
	second := BuildUniversalBase("/tmp/ws")
	if first != second {
		t.Fatal("BuildUniversalBase must be byte-stable for the same input")
	}
	if !strings.Contains(first, "/tmp/ws") {
		t.Errorf("base must contain cwd, got %q", first[:200])
	}
	// Neutral identity preamble — leader-specific framing must NOT leak here.
	if strings.Contains(first, "expert coding assistant") {
		t.Error("universal base must stay role-neutral; leader identity leaked")
	}
	// Tool inventory belongs to the role block, not the shared base
	// (leader/teammate tool sets differ, so listing tools here would break
	// cross-agent byte equality).
	if strings.Contains(first, "## Tools") {
		t.Error("universal base must not include the tool inventory")
	}
}

// TestBuildLeaderAndTeammateShareUniversalBase enforces that leader and
// teammate render the same universal base — this is the cache-key contract.
// If this drifts, every teammate spawn after warm-up loses its base
// cache_read.
func TestBuildLeaderAndTeammateShareUniversalBase(t *testing.T) {
	t.Parallel()

	cwd := "/tmp/ws"
	// Leader path: identity returned by BuildFrozenSystemParts is the
	// universal base under the hood.
	leaderBase, _ := BuildFrozenSystemParts(cwd, ContextFiles{}, []ToolInfo{
		{Name: "read"}, {Name: "task_create"}, {Name: "subagent"},
	})
	// Teammate path: spawner reuses BuildUniversalBase directly (no tools
	// argument — base is intentionally tool-agnostic).
	teammateBase := BuildUniversalBase(cwd)
	if leaderBase != teammateBase {
		t.Fatal("leader and teammate must share the exact same universal base bytes — cache will not hit otherwise")
	}
}

// TestBuildLeaderRoleBlock_ToolGating covers the conditional sections that
// only render when the corresponding tools are wired.
func TestBuildLeaderRoleBlock_ToolGating(t *testing.T) {
	t.Parallel()

	// No conditional tools → identity + (no Task/Team sections).
	bare := BuildLeaderRoleBlock(ContextFiles{}, []ToolInfo{{Name: "read"}})
	if strings.Contains(bare, "## Task Management") {
		t.Error("Task Management leaked without task_* tools")
	}
	if strings.Contains(bare, "## Team coordination") {
		t.Error("Team coordination leaked without team tools")
	}
	if !strings.Contains(bare, "expert coding assistant") {
		t.Error("leader identity missing")
	}

	// Full task-management toolset → section appears.
	withTasks := BuildLeaderRoleBlock(ContextFiles{}, []ToolInfo{
		{Name: "task_create"}, {Name: "task_update"}, {Name: "task_list"},
	})
	if !strings.Contains(withTasks, "## Task Management") {
		t.Error("Task Management should render with full task toolset")
	}

	// Full team toolset → section appears.
	withTeam := BuildLeaderRoleBlock(ContextFiles{}, []ToolInfo{
		{Name: "team_dismiss"}, {Name: "send_message"}, {Name: "subagent"},
	})
	if !strings.Contains(withTeam, "## Team coordination") {
		t.Error("Team coordination should render with full team toolset")
	}
}

// TestBuildTeammateRoleBlock_OmitsCustomMarkerWhenEmpty makes sure the
// "# Custom Agent Instructions" header does not appear when the agent
// definition has no custom prompt — an empty heading would be a useless
// section and would also bloat the cache fingerprint.
func TestBuildTeammateRoleBlock_OmitsCustomMarkerWhenEmpty(t *testing.T) {
	t.Parallel()

	got := BuildTeammateRoleBlock(nil, "")
	if strings.Contains(got, "Custom Agent Instructions") {
		t.Error("empty agentRolePrompt must not emit the custom-instructions header")
	}
	if !strings.Contains(got, "Mailbox") {
		t.Error("mailbox addendum missing")
	}
	if !strings.Contains(got, "team lead") {
		t.Error("teammate identity preamble missing")
	}
}

// TestBuildTeammateRoleBlock_AppendsCustomPrompt verifies the standard
// composition path: identity → tools → mailbox → custom agent. The
// custom-instructions header must be H1 ("# Custom Agent Instructions") —
// H2 would silently change the prompt cache fingerprint.
func TestBuildTeammateRoleBlock_AppendsCustomPrompt(t *testing.T) {
	t.Parallel()

	got := BuildTeammateRoleBlock(
		[]ToolInfo{{Name: "read", Description: "Read files"}, {Name: "send_message", Description: "Send"}},
		"You are a researcher. Cite sources.",
	)
	for _, marker := range []string{
		"team lead",                  // identity
		"## Tools",                   // tool inventory
		"**read**",                   // a specific tool
		"## Mailbox & Coordination",  // addendum
		"\n# Custom Agent Instructions\n", // H1 wrapper with the leading newline
		"You are a researcher",       // role prompt body
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("teammate role block missing %q", marker)
		}
	}
	// Guard against a future regression to H2.
	if strings.Contains(got, "## Custom Agent Instructions") {
		t.Error("custom-instructions header must be H1, not H2")
	}
	// Ordering: identity comes before tools, tools before mailbox, mailbox
	// before custom. A reordering would change the bytes and break cache.
	wantOrder := []string{"team lead", "## Tools", "## Mailbox & Coordination", "# Custom Agent Instructions"}
	prev := -1
	for _, m := range wantOrder {
		idx := strings.Index(got, m)
		if idx <= prev {
			t.Fatalf("section %q appeared out of order (got=%d, previous=%d)", m, idx, prev)
		}
		prev = idx
	}
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
