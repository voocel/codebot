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
		"message": "You are now in plan mode. Explore the codebase and write a detailed implementation plan. Write the full plan as text, then call exit_plan_mode with title and content.",
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
	return "Submit your completed implementation plan for user review. " +
		"You MUST write the full plan as text in the conversation first so the user can see it, " +
		"then call this tool with the same content in the 'content' parameter."
}

func (t *exitPlanModeTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("title", schema.String("Short title summarizing the plan (under 60 chars)")).Required(),
		schema.Property("content", schema.String("The full plan content (markdown). Must match the text you just wrote.")).Required(),
	)
}

func (t *exitPlanModeTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	_ = json.Unmarshal(args, &a)
	if a.Title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if a.Content == "" {
		return nil, fmt.Errorf("content is required — write your plan first, then pass it here")
	}
	return json.Marshal(map[string]string{
		"status":  "submitted",
		"title":   a.Title,
		"message": "Plan submitted for review. Do not respond further.",
	})
}
