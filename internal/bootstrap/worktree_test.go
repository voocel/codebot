package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	localtools "github.com/voocel/codebot/internal/tools"
)

// TestWireWorktreeTools_BindsRuntimeBackend: the tools reach the live Runtime,
// not their unwired stub. Asserts on backend guard errors ("git repository" /
// "not in a worktree"), which the "not wired" stub can never produce.
func TestWireWorktreeTools_BindsRuntimeBackend(t *testing.T) {
	enter := localtools.NewEnterWorktree()
	exit := localtools.NewExitWorktree()
	rt := &Runtime{Cwd: t.TempDir()} // temp dir is not a git repo; activeWorktree nil

	wireWorktreeTools([]agentcore.Tool{enter, exit, &fakeTool{name: "other"}}, rt)

	if _, err := enter.Execute(context.Background(), []byte(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "git repository") {
		t.Fatalf("enter_worktree should hit the Runtime git-repo guard, got %v", err)
	}
	if _, err := exit.Execute(context.Background(), []byte(`{"action":"keep"}`)); err == nil ||
		!strings.Contains(err.Error(), "not in a worktree") {
		t.Fatalf("exit_worktree should hit the Runtime not-in-worktree guard, got %v", err)
	}
}

// TestWorktreeToolsAreDeferred guards that the worktree tools stay out of the
// always-visible core set, so they defer behind tool_search like CC's
// shouldDefer: true rather than bloating the base prompt.
func TestWorktreeToolsAreDeferred(t *testing.T) {
	for _, name := range []string{"enter_worktree", "exit_worktree"} {
		if coreToolNames[name] {
			t.Errorf("%s must NOT be in coreToolNames (should stay deferred)", name)
		}
	}
}
