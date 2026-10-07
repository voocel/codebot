package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
)

func NewEnterWorktree(enter func(name string) (dir string, err error)) agentcore.Tool {
	tool := agentcore.NewTool("enter_worktree",
		`Create an isolated git worktree and switch the session into it, so edits are sandboxed from the main working tree. Use ONLY when the user explicitly asks to work in a worktree (e.g. "start a worktree", "use a worktree"); do NOT call this proactively for ordinary feature or bugfix work — use the normal git workflow instead. Requires a git repository and that the session is not already in a worktree; the tool returns an error otherwise. Call exit_worktree to leave.`,
		schema.Object(
			schema.Property("name", schema.String("Optional name for the worktree; \"scratch\" if omitted.")),
		),
		func(_ context.Context, a struct {
			Name string `json:"name"`
		}) (agentcore.Result, error) {
			dir, err := enter(strings.TrimSpace(a.Name))
			if err != nil {
				return agentcore.Result{}, err
			}
			return agentcore.TextResult(fmt.Sprintf("Entered worktree sandbox at %s. Edits here are isolated from the main workspace; call exit_worktree to leave.", dir)), nil
		},
	)
	tool.Label = "Enter Worktree"
	return tool
}

func NewExitWorktree(exit func(discard bool) (message string, err error)) agentcore.Tool {
	tool := agentcore.NewTool("exit_worktree",
		`Exit the worktree created by enter_worktree and return the session to the main workspace. Use ONLY when the user explicitly asks to exit or leave the worktree; do NOT call this proactively. Returns an error if the session is not in a worktree.`,
		schema.Object(
			schema.Property("action", schema.Enum(`"keep" exits but preserves uncommitted changes for review (a clean sandbox is cleaned up automatically); "discard" deletes the worktree and throws the changes away.`, "keep", "discard")).Required(),
		),
		func(_ context.Context, a struct {
			Action string `json:"action"`
		}) (agentcore.Result, error) {
			var discard bool
			switch a.Action {
			case "keep":
				discard = false
			case "discard":
				discard = true
			default:
				return agentcore.Result{}, fmt.Errorf("action must be %q or %q", "keep", "discard")
			}
			msg, err := exit(discard)
			if err != nil {
				return agentcore.Result{}, err
			}
			return agentcore.TextResult(msg), nil
		},
	)
	tool.Label = "Exit Worktree"
	return tool
}
