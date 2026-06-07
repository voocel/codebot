package storage

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestBuildContextSnapshotIncludesModelProviderAndThinking(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := create(dir, "/workspace/project")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	if err := store.AppendModelChange("anthropic", "claude-sonnet-4-5"); err != nil {
		t.Fatalf("append model change: %v", err)
	}
	if err := store.AppendThinkingLevelChange("high"); err != nil {
		t.Fatalf("append thinking change: %v", err)
	}
	if err := store.AppendMessage(agentcore.UserMsg("hello")); err != nil {
		t.Fatalf("append message: %v", err)
	}

	snapshot, err := store.BuildSnapshot()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}

	if snapshot.Provider != "anthropic" {
		t.Fatalf("provider = %q, want %q", snapshot.Provider, "anthropic")
	}
	if snapshot.Model != "claude-sonnet-4-5" {
		t.Fatalf("model = %q, want %q", snapshot.Model, "claude-sonnet-4-5")
	}
	if snapshot.Thinking != "high" {
		t.Fatalf("thinking = %q, want %q", snapshot.Thinking, "high")
	}
	if len(snapshot.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(snapshot.Messages))
	}
}

func TestBuildSnapshotRepairsMissingToolResult(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := create(dir, "/workspace/project")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	assistant := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ToolCallBlock(agentcore.ToolCall{
				ID:   "toolu_123",
				Name: "read",
				Args: []byte(`{"file_path":"README.md"}`),
			}),
		},
	}
	if err := store.AppendMessage(assistant); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if err := store.AppendMessage(agentcore.UserMsg("继续")); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	snapshot, err := store.BuildSnapshot()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(snapshot.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(snapshot.Messages))
	}

	toolMsg, ok := snapshot.Messages[1].(agentcore.Message)
	if !ok {
		t.Fatalf("snapshot message[1] type = %T, want agentcore.Message", snapshot.Messages[1])
	}
	if toolMsg.Role != agentcore.RoleTool {
		t.Fatalf("message[1] role = %s, want %s", toolMsg.Role, agentcore.RoleTool)
	}
	if got := toolMsg.Metadata["tool_call_id"]; got != "toolu_123" {
		t.Fatalf("tool_call_id = %v, want toolu_123", got)
	}
	if got := toolMsg.Metadata["is_error"]; got != true {
		t.Fatalf("is_error = %v, want true", got)
	}
}

func TestBuildSnapshotPreservesPreCompactionStateWhileSkippingMessages(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := create(dir, "/workspace/project")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	// State entries appear ONCE before the compaction; nothing resets them
	// after. If chain truncation discards them, the snapshot loses them.
	if err := store.AppendModelChange("anthropic", "claude-sonnet-4-5"); err != nil {
		t.Fatalf("append model change: %v", err)
	}
	if err := store.AppendThinkingLevelChange("high"); err != nil {
		t.Fatalf("append thinking change: %v", err)
	}
	if err := store.AppendPlanState("planning", "calm-river", "balanced"); err != nil {
		t.Fatalf("append plan state: %v", err)
	}

	// Pre-compaction conversation: should be discarded by the snapshot.
	for i := 0; i < 5; i++ {
		if err := store.AppendMessage(agentcore.UserMsg("pre-compaction noise")); err != nil {
			t.Fatalf("append pre msg: %v", err)
		}
	}

	if err := store.AppendCompaction("summary of earlier work", nil); err != nil {
		t.Fatalf("append compaction: %v", err)
	}

	// Post-compaction tail.
	if err := store.AppendMessage(agentcore.UserMsg("post-compaction live message")); err != nil {
		t.Fatalf("append post msg: %v", err)
	}

	snapshot, err := store.BuildSnapshot()
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}

	// State must survive even though it was written before the compaction.
	if snapshot.Provider != "anthropic" || snapshot.Model != "claude-sonnet-4-5" {
		t.Fatalf("pre-compaction model lost: provider=%q model=%q", snapshot.Provider, snapshot.Model)
	}
	if snapshot.Thinking != "high" {
		t.Fatalf("pre-compaction thinking lost: %q", snapshot.Thinking)
	}
	if snapshot.PlanPhase != "planning" || snapshot.PlanSlug != "calm-river" || snapshot.PlanPreMode != "balanced" {
		t.Fatalf("pre-compaction plan state lost: %+v", snapshot)
	}

	// Pre-compaction messages must be gone; only summary + post-compaction tail.
	// Snapshot.Messages = [summary user msg, post-compaction user msg] = 2.
	if len(snapshot.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2 (summary + post-compaction tail)", len(snapshot.Messages))
	}
	first, ok := snapshot.Messages[0].(agentcore.Message)
	if !ok {
		t.Fatalf("messages[0] type = %T, want agentcore.Message", snapshot.Messages[0])
	}
	if !strings.Contains(first.TextContent(), "summary of earlier work") {
		t.Fatalf("messages[0] should be compaction summary, got %q", first.TextContent())
	}
	last, ok := snapshot.Messages[1].(agentcore.Message)
	if !ok {
		t.Fatalf("messages[1] type = %T, want agentcore.Message", snapshot.Messages[1])
	}
	if !strings.Contains(last.TextContent(), "post-compaction live message") {
		t.Fatalf("messages[1] should be post-compaction tail, got %q", last.TextContent())
	}
}

func TestBuildSnapshotIncludesPlanState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := create(dir, "/workspace/project")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	if err := store.AppendPlanState("planning", "calm-writing-river", "balanced"); err != nil {
		t.Fatalf("append plan state: %v", err)
	}

	snapshot, err := store.BuildSnapshot()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if snapshot.PlanPhase != "planning" {
		t.Fatalf("plan phase = %q, want planning", snapshot.PlanPhase)
	}
	if snapshot.PlanSlug != "calm-writing-river" {
		t.Fatalf("plan slug = %q, want calm-writing-river", snapshot.PlanSlug)
	}
	if snapshot.PlanPreMode != "balanced" {
		t.Fatalf("plan pre mode = %q, want balanced", snapshot.PlanPreMode)
	}
}

func TestBuildSnapshotIncludesGoalState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := create(dir, "/workspace/project")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	if err := store.AppendGoalState(GoalStateEntry{
		ID:        "steady-goal",
		Objective: "finish the feature",
		Status:    "active",
		Reason:    "still running",
	}); err != nil {
		t.Fatalf("append goal state: %v", err)
	}

	snapshot, err := store.BuildSnapshot()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if snapshot.Goal.ID != "steady-goal" {
		t.Fatalf("goal id = %q, want steady-goal", snapshot.Goal.ID)
	}
	if snapshot.Goal.Objective != "finish the feature" {
		t.Fatalf("goal objective = %q", snapshot.Goal.Objective)
	}
	if snapshot.Goal.Status != "active" {
		t.Fatalf("goal status = %q, want active", snapshot.Goal.Status)
	}
}
