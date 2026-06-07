package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/codebot/internal/goal"
)

func TestGoalToolsGetAndUpdate(t *testing.T) {
	t.Parallel()

	manager := goal.NewManager(nil, nil)
	if _, err := manager.Create("finish tests"); err != nil {
		t.Fatalf("create goal: %v", err)
	}

	getTool := NewGoalGet()
	getTool.SetSnapshotter(manager.Snapshot)
	raw, err := getTool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("get execute: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if got["status"] != "active" || got["objective"] != "finish tests" {
		t.Fatalf("unexpected get result: %s", raw)
	}

	updateTool := NewGoalUpdate()
	updateTool.SetHandlers(manager.Complete, manager.Block)
	raw, err = updateTool.Execute(context.Background(), json.RawMessage(`{"status":"complete","reason":"verified"}`))
	if err != nil {
		t.Fatalf("update execute: %v", err)
	}
	if !strings.Contains(string(raw), `"status":"complete"`) {
		t.Fatalf("expected complete status, got %s", raw)
	}
}

func TestGoalUpdateBlockedRequiresReason(t *testing.T) {
	t.Parallel()

	manager := goal.NewManager(nil, nil)
	if _, err := manager.Create("finish tests"); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	updateTool := NewGoalUpdate()
	updateTool.SetHandlers(manager.Complete, manager.Block)

	if _, err := updateTool.Execute(context.Background(), json.RawMessage(`{"status":"blocked"}`)); err == nil {
		t.Fatal("expected blocked update without reason to fail")
	}
}

func TestGoalGetRequiresWiring(t *testing.T) {
	t.Parallel()

	if _, err := NewGoalGet().Execute(context.Background(), nil); err == nil {
		t.Fatal("expected unwired get_goal to fail")
	}
}
