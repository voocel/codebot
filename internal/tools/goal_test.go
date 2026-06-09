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

func TestGoalCreateTool(t *testing.T) {
	t.Parallel()

	manager := goal.NewManager(nil, nil)
	createTool := NewGoalCreate()
	createTool.SetCreator(manager.CreateWithBudget)

	raw, err := createTool.Execute(context.Background(), json.RawMessage(`{"objective":"finish tests","token_budget":1200}`))
	if err != nil {
		t.Fatalf("create execute: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}
	if got["status"] != "active" || got["objective"] != "finish tests" {
		t.Fatalf("unexpected create result: %s", raw)
	}
	if got["token_budget"] != float64(1200) {
		t.Fatalf("token_budget = %v, want 1200", got["token_budget"])
	}
}

func TestGoalCreateToolRejectsExistingGoal(t *testing.T) {
	t.Parallel()

	manager := goal.NewManager(nil, nil)
	if _, err := manager.Create("finish tests"); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if _, err := manager.Complete("done"); err != nil {
		t.Fatalf("complete goal: %v", err)
	}

	createTool := NewGoalCreate()
	createTool.SetCreator(manager.CreateWithBudget)
	if _, err := createTool.Execute(context.Background(), json.RawMessage(`{"objective":"second goal"}`)); err == nil {
		t.Fatal("expected create_goal to reject any existing goal")
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

func TestGoalCreateRequiresWiring(t *testing.T) {
	t.Parallel()

	if _, err := NewGoalCreate().Execute(context.Background(), json.RawMessage(`{"objective":"x"}`)); err == nil {
		t.Fatal("expected unwired create_goal to fail")
	}
}
