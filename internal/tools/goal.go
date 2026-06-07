package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/permission"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/codebot/internal/goal"
)

type GoalGetTool struct {
	snapshot func() goal.State
}

func NewGoalGet() *GoalGetTool { return &GoalGetTool{} }

func (t *GoalGetTool) SetSnapshotter(fn func() goal.State) { t.snapshot = fn }

func (t *GoalGetTool) Name() string                           { return "get_goal" }
func (t *GoalGetTool) Label() string                          { return "Get Goal" }
func (t *GoalGetTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *GoalGetTool) ConcurrencySafe(_ json.RawMessage) bool { return true }
func (t *GoalGetTool) PermissionMetadata() permission.Metadata {
	return permission.Metadata{Capability: permission.CapabilityRead}
}
func (t *GoalGetTool) Description() string {
	return `Use this tool to inspect the explicit session goal created by the user with /goal. If no goal is active, the result reports status "off".`
}
func (t *GoalGetTool) Schema() map[string]any { return schema.Object() }

func (t *GoalGetTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	if t.snapshot == nil {
		return nil, fmt.Errorf("get_goal is not wired")
	}
	return json.Marshal(goalToolState(t.snapshot()))
}

type GoalUpdateTool struct {
	complete func(reason string) (goal.State, error)
	block    func(reason string) (goal.State, error)
}

func NewGoalUpdate() *GoalUpdateTool { return &GoalUpdateTool{} }

func (t *GoalUpdateTool) SetHandlers(complete, block func(string) (goal.State, error)) {
	t.complete = complete
	t.block = block
}

func (t *GoalUpdateTool) Name() string                           { return "update_goal" }
func (t *GoalUpdateTool) Label() string                          { return "Update Goal" }
func (t *GoalUpdateTool) ConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *GoalUpdateTool) PermissionMetadata() permission.Metadata {
	return permission.Metadata{Capability: permission.CapabilityInternal}
}
func (t *GoalUpdateTool) Description() string {
	return `Use this tool only when working on an explicit /goal. Mark the goal "complete" only when the objective is fully achieved and verified. Mark it "blocked" only when the same blocker has recurred for at least three consecutive goal turns and you cannot make meaningful progress without user input or an external change; include the blocker in reason.`
}
func (t *GoalUpdateTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("status", schema.Enum("New goal status", "complete", "blocked")).Required(),
		schema.Property("reason", schema.String("Optional completion note, or required blocker explanation when status is blocked")),
	)
}

func (t *GoalUpdateTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	var (
		state goal.State
		err   error
	)
	switch goal.Status(a.Status) {
	case goal.StatusComplete:
		if t.complete == nil {
			return nil, fmt.Errorf("update_goal complete handler is not wired")
		}
		state, err = t.complete(a.Reason)
	case goal.StatusBlocked:
		if t.block == nil {
			return nil, fmt.Errorf("update_goal block handler is not wired")
		}
		state, err = t.block(a.Reason)
	default:
		return nil, fmt.Errorf("status must be %q or %q", goal.StatusComplete, goal.StatusBlocked)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(goalToolState(state))
}

func goalToolState(state goal.State) map[string]any {
	state = state.Normalize()
	result := map[string]any{
		"id":        state.ID,
		"objective": state.Objective,
		"status":    string(state.Status),
	}
	if !state.CreatedAt.IsZero() {
		result["created_at"] = state.CreatedAt
	}
	if !state.UpdatedAt.IsZero() {
		result["updated_at"] = state.UpdatedAt
	}
	if !state.CompletedAt.IsZero() {
		result["completed_at"] = state.CompletedAt
	}
	if !state.BlockedAt.IsZero() {
		result["blocked_at"] = state.BlockedAt
	}
	if !state.BudgetLimitedAt.IsZero() {
		result["budget_limited_at"] = state.BudgetLimitedAt
	}
	if state.Reason != "" {
		result["reason"] = state.Reason
	}
	if state.BlockedReason != "" {
		result["blocked_reason"] = state.BlockedReason
		result["blocked_count"] = state.BlockedCount
	}
	result["tokens_used"] = state.TokensUsed
	if state.TokenBudget > 0 {
		remaining := state.TokenBudget - state.TokensUsed
		if remaining < 0 {
			remaining = 0
		}
		result["token_budget"] = state.TokenBudget
		result["tokens_remaining"] = remaining
	}
	return result
}
