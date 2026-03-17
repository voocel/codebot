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
