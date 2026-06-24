package approval

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/voocel/agentcore/permission"
	agentcoretools "github.com/voocel/agentcore/tools"
)

// TestDecideThreadsLiveCwdAsWorkspace: Decide resolves a relative path against
// the live cwd on the context (the worktree), not the engine's boot cwd — so
// the audited path matches where the tool runs.
func TestDecideThreadsLiveCwdAsWorkspace(t *testing.T) {
	main := t.TempDir()
	wt := t.TempDir()
	var summaries []string
	engine, err := NewEngine(main, ModeTrust, nil, func(e AuditEntry) {
		summaries = append(summaries, e.Summary)
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	// Mirror EnterWorktree: make both the main repo and the worktree writable.
	engine.SetFilesystemRoots(FilesystemRoots{
		ReadRoots:  []string{main, wt},
		WriteRoots: []string{main, wt},
	})

	write := func(ctx context.Context) {
		req := permission.Request{
			ToolName: "write",
			Args:     []byte(`{"file_path":"a.txt"}`),
		}
		if _, err := engine.Decide(ctx, req); err != nil {
			t.Fatalf("Decide: %v", err)
		}
	}

	// Bare context → no cwd override → resolves against the boot cwd (main).
	write(context.Background())
	// Context carrying the worktree cwd (as Session.runCtx threads after
	// enter_worktree) → resolves against the worktree.
	write(agentcoretools.WithCwd(context.Background(), wt))

	if len(summaries) != 2 {
		t.Fatalf("want 2 audit entries, got %d", len(summaries))
	}
	if want := filepath.Join(main, "a.txt"); summaries[0] != want {
		t.Errorf("no-override summary = %q, want %q", summaries[0], want)
	}
	if want := filepath.Join(wt, "a.txt"); summaries[1] != want {
		t.Errorf("live-cwd summary = %q, want %q (worktree path)", summaries[1], want)
	}
}

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

