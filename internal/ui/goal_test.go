package ui

import (
	"testing"

	"github.com/voocel/codebot/internal/goal"
)

func TestStatusGoalShowsActiveGoal(t *testing.T) {
	t.Parallel()

	manager := goal.NewManager(nil, nil)
	if _, err := manager.Create("finish the goal status chip"); err != nil {
		t.Fatalf("create: %v", err)
	}
	app := &App{GoalManager: manager}

	if got := app.statusGoal(nil); got != "goal: finish the goal status chip" {
		t.Fatalf("status goal = %q", got)
	}
}

func TestStatusGoalShowsBudgetRemaining(t *testing.T) {
	t.Parallel()

	manager := goal.NewManager(nil, nil)
	state, err := manager.CreateWithBudget("finish the goal status chip", 1200)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.TokensUsed = 200
	if err := manager.Restore(state); err != nil {
		t.Fatalf("restore: %v", err)
	}
	app := &App{GoalManager: manager}

	if got := app.statusGoal(nil); got != "goal: 1k left" {
		t.Fatalf("status goal = %q", got)
	}
}

func TestStatusGoalShowsUsageLimited(t *testing.T) {
	t.Parallel()

	manager := goal.NewManager(nil, nil)
	if _, err := manager.Create("finish the goal status chip"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.UsageLimit(""); err != nil {
		t.Fatalf("usage limit: %v", err)
	}
	app := &App{GoalManager: manager}

	if got := app.statusGoal(nil); got != "goal: usage" {
		t.Fatalf("status goal = %q", got)
	}
}
