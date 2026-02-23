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
