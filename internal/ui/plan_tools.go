package ui

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
)

// ---------------------------------------------------------------------------
// enter_plan_mode
// ---------------------------------------------------------------------------

type enterPlanModeTool struct{}

func newEnterPlanModeTool() *enterPlanModeTool { return &enterPlanModeTool{} }

func (t *enterPlanModeTool) Name() string  { return "enter_plan_mode" }
func (t *enterPlanModeTool) Label() string { return "Enter Plan Mode" }
func (t *enterPlanModeTool) Description() string {
	return "Enter plan mode to explore the codebase and design an implementation plan before making changes. " +
		"Use this proactively when a task is non-trivial and benefits from planning first."
}

func (t *enterPlanModeTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("task", schema.String("Brief description of the task to plan for")),
	)
}

func (t *enterPlanModeTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Task string `json:"task"`
	}
	_ = json.Unmarshal(args, &a)
	return json.Marshal(map[string]string{
		"status":  "entered",
		"message": "You are now in plan mode. Explore the codebase and write a detailed implementation plan. When done, call exit_plan_mode.",
	})
}

// ---------------------------------------------------------------------------
// exit_plan_mode
// ---------------------------------------------------------------------------

type exitPlanModeTool struct{}

func newExitPlanModeTool() *exitPlanModeTool { return &exitPlanModeTool{} }

func (t *exitPlanModeTool) Name() string  { return "exit_plan_mode" }
func (t *exitPlanModeTool) Label() string { return "Exit Plan Mode" }
func (t *exitPlanModeTool) Description() string {
	return "Signal that your implementation plan is complete and ready for user review. " +
		"Call this after writing your plan as text in the conversation."
}

func (t *exitPlanModeTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("title", schema.String("Short title summarizing the plan (under 60 chars)")),
	)
}

func (t *exitPlanModeTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(args, &a)
	if a.Title == "" {
		return nil, fmt.Errorf("title is required")
	}
	return json.Marshal(map[string]string{
		"status":  "submitted",
		"title":   a.Title,
		"message": "Plan submitted for review. Do not respond further.",
	})
}
