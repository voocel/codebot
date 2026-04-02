package approval

import (
	"context"
	"testing"
)

func TestApproveHookAllowAlwaysPersists(t *testing.T) {
	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	calls := 0
	engine.SetApprover(func(context.Context, Prompt) (Choice, error) {
		calls++
		return ChoiceAllowAlways, nil
	})

	req := HookRequest{
		Event:    "PreToolUse",
		Tool:     "bash",
		Command:  "echo ok",
		Blocking: true,
	}
	if err := engine.ApproveHook(context.Background(), req); err != nil {
		t.Fatalf("ApproveHook first: %v", err)
	}

	engine.SetApprover(func(context.Context, Prompt) (Choice, error) {
		t.Fatal("persisted approval should skip prompt")
		return ChoiceDeny, nil
	})
	if err := engine.ApproveHook(context.Background(), req); err != nil {
		t.Fatalf("ApproveHook second: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected one approval prompt, got %d", calls)
	}
}

func TestApproveCommandRespectsPlanMode(t *testing.T) {
	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetPlanMode(true)

	if err := engine.ApproveCommand(context.Background(), CommandRequest{
		Name:     "status",
		Category: CommandCategoryInfo,
	}); err != nil {
		t.Fatalf("info command should pass in plan mode: %v", err)
	}

	if err := engine.ApproveCommand(context.Background(), CommandRequest{
		Name:     "session",
		Category: CommandCategorySession,
	}); err == nil {
		t.Fatal("session command should be denied in plan mode")
	}
}
