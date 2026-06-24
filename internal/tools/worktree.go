package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/permission"
	"github.com/voocel/agentcore/schema"
)

// EnterWorktreeTool moves the session into an isolated git worktree sandbox —
// the model-driven counterpart to the /worktree command. The backend is
// injected via SetEnter (this package can't import bootstrap).
type EnterWorktreeTool struct {
	enter func(name string) (dir string, err error)
}

func NewEnterWorktree() *EnterWorktreeTool { return &EnterWorktreeTool{} }

func (t *EnterWorktreeTool) SetEnter(fn func(string) (string, error)) { t.enter = fn }

func (t *EnterWorktreeTool) Name() string                           { return "enter_worktree" }
func (t *EnterWorktreeTool) Label() string                          { return "Enter Worktree" }
func (t *EnterWorktreeTool) ConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *EnterWorktreeTool) PermissionMetadata() permission.Metadata {
	return permission.Metadata{Capability: permission.CapabilityInternal}
}
func (t *EnterWorktreeTool) Description() string {
	return `Create an isolated git worktree and switch the session into it, so edits are sandboxed from the main working tree. Use ONLY when the user explicitly asks to work in a worktree (e.g. "start a worktree", "use a worktree"); do NOT call this proactively for ordinary feature or bugfix work — use the normal git workflow instead. Requires a git repository, no active teammates, and that the session is not already in a worktree; the tool returns an error otherwise. Call exit_worktree to leave.`
}
func (t *EnterWorktreeTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("name", schema.String("Optional name for the worktree. A random name is generated if omitted.")),
	)
}

func (t *EnterWorktreeTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	if t.enter == nil {
		return nil, fmt.Errorf("enter_worktree handler is not wired")
	}
	var a struct {
		Name string `json:"name"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("invalid args: %w", err)
		}
	}
	dir, err := t.enter(strings.TrimSpace(a.Name))
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"worktree_path": dir,
		"message":       fmt.Sprintf("Entered worktree sandbox at %s. Edits here are isolated from the main workspace; call exit_worktree to leave.", dir),
	})
}

// ExitWorktreeTool returns the session to the main workspace: "keep" preserves
// uncommitted changes (a clean sandbox is auto-removed), "discard" deletes it.
// Maps 1:1 onto Runtime.ExitWorktree(discard bool), wired via SetExit.
type ExitWorktreeTool struct {
	exit func(discard bool) (message string, err error)
}

func NewExitWorktree() *ExitWorktreeTool { return &ExitWorktreeTool{} }

func (t *ExitWorktreeTool) SetExit(fn func(bool) (string, error)) { t.exit = fn }

func (t *ExitWorktreeTool) Name() string                           { return "exit_worktree" }
func (t *ExitWorktreeTool) Label() string                          { return "Exit Worktree" }
func (t *ExitWorktreeTool) ConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *ExitWorktreeTool) PermissionMetadata() permission.Metadata {
	return permission.Metadata{Capability: permission.CapabilityInternal}
}
func (t *ExitWorktreeTool) Description() string {
	return `Exit the worktree created by enter_worktree and return the session to the main workspace. Use ONLY when the user explicitly asks to exit or leave the worktree; do NOT call this proactively. Returns an error if the session is not in a worktree or teammates are still active.`
}
func (t *ExitWorktreeTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("action", schema.Enum(`"keep" exits but preserves uncommitted changes for review (a clean sandbox is cleaned up automatically); "discard" deletes the worktree and throws the changes away.`, "keep", "discard")).Required(),
	)
}

func (t *ExitWorktreeTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	if t.exit == nil {
		return nil, fmt.Errorf("exit_worktree handler is not wired")
	}
	var a struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	var discard bool
	switch a.Action {
	case "keep":
		discard = false
	case "discard":
		discard = true
	default:
		return nil, fmt.Errorf("action must be %q or %q", "keep", "discard")
	}
	msg, err := t.exit(discard)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"message": msg})
}
