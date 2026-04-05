package storage

import (
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
				Args: []byte(`{"path":"README.md"}`),
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

func TestBuildSnapshotIncludesPlanState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := create(dir, "/workspace/project")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	if err := store.AppendPlanState("review", "calm-writing-river", "Refactor plan", "balanced", []AllowedCommandEntry{
		{CommandPrefix: "go test", Description: "运行测试"},
		{CommandPrefix: "go build", Description: "构建项目"},
	}); err != nil {
		t.Fatalf("append plan state: %v", err)
	}

	snapshot, err := store.BuildSnapshot()
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if snapshot.PlanPhase != "review" {
		t.Fatalf("plan phase = %q, want review", snapshot.PlanPhase)
	}
	if snapshot.PlanSlug != "calm-writing-river" {
		t.Fatalf("plan slug = %q, want calm-writing-river", snapshot.PlanSlug)
	}
	if snapshot.PlanTitle != "Refactor plan" {
		t.Fatalf("plan title = %q, want Refactor plan", snapshot.PlanTitle)
	}
	if snapshot.PlanPreMode != "balanced" {
		t.Fatalf("plan pre mode = %q, want balanced", snapshot.PlanPreMode)
	}
	if len(snapshot.PlanAllowedCommands) != 2 {
		t.Fatalf("plan allowed commands len = %d, want 2", len(snapshot.PlanAllowedCommands))
	}
	if snapshot.PlanAllowedCommands[0].CommandPrefix != "go test" {
		t.Fatalf("first allowed command = %q, want go test", snapshot.PlanAllowedCommands[0].CommandPrefix)
	}
}
