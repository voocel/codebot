package approval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/agentcore/permission"
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

func TestPlanModeAllowsControlPlaneTools(t *testing.T) {
	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetPlanMode(true)

	for _, name := range planModeAllowedTools {
		decision, err := engine.Decide(context.Background(), permission.Request{
			ToolName: name,
			Metadata: permission.Metadata{Capability: permission.CapabilityInternal},
		})
		if err != nil {
			t.Fatalf("Decide %s: %v", name, err)
		}
		if decision == nil || !decision.Allowed() {
			t.Fatalf("expected %s to be allowed in plan mode, got %#v", name, decision)
		}
	}

	// An unlisted Internal tool stays blocked — the allowlist is exhaustive.
	decision, err := engine.Decide(context.Background(), permission.Request{
		ToolName: "task_create",
		Metadata: permission.Metadata{Capability: permission.CapabilityInternal},
	})
	if err != nil {
		t.Fatalf("Decide task_create: %v", err)
	}
	if decision == nil || decision.Allowed() {
		t.Fatalf("expected task_create to be denied in plan mode, got %#v", decision)
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

func TestApprovedPlanActionsAllowMatchingBash(t *testing.T) {
	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetPlanAllowedCommands([]string{"go test"})

	decision, err := engine.Decide(context.Background(), permission.Request{
		ToolName: "bash",
		Args:     json.RawMessage(`{"command":"go test ./..."}`),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || !decision.Allowed() {
		t.Fatalf("expected allow decision, got %#v", decision)
	}
	if decision.Reason != "allowed by approved plan" {
		t.Fatalf("reason = %q", decision.Reason)
	}

	decision, err = engine.Decide(context.Background(), permission.Request{
		ToolName: "bash",
		Args:     json.RawMessage(`{"command":"go build ./..."}`),
	})
	if err != nil {
		t.Fatalf("Decide build: %v", err)
	}
	if decision == nil || decision.Allowed() {
		t.Fatalf("expected deny decision for unapproved command, got %#v", decision)
	}
}

func TestApprovedPlanActionsRespectWorkdirRoots(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()

	engine, err := NewEngine(workspace, ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetPlanAllowedCommands([]string{"go test"})

	decision, err := engine.Decide(context.Background(), permission.Request{
		ToolName: "bash",
		Args: json.RawMessage(`{
			"command":"go test ./...",
			"workdir":"` + outside + `"
		}`),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Allowed() {
		t.Fatalf("expected deny decision for outside workdir, got %#v", decision)
	}
}

func TestApprovedPlanActionsDoNotBypassDenyRules(t *testing.T) {
	rules, err := ParseRuleSet(nil, []string{"Bash(go test)", "bash"})
	if err != nil {
		t.Fatalf("ParseRuleSet: %v", err)
	}

	engine, err := NewEngine(t.TempDir(), ModeBalanced, rules, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetPlanAllowedCommands([]string{"go test"})

	decision, err := engine.Decide(context.Background(), permission.Request{
		ToolName: "bash",
		Args:     json.RawMessage(`{"command":"go test ./..."}`),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || decision.Allowed() {
		t.Fatalf("expected deny decision, got %#v", decision)
	}
	if decision.Source != permission.DecisionSourceRule {
		t.Fatalf("source = %q, want %q", decision.Source, permission.DecisionSourceRule)
	}
}
