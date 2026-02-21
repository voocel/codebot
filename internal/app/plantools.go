package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
)

// ---------------------------------------------------------------------------
// enter_plan_mode — LLM calls this to enter plan mode
// ---------------------------------------------------------------------------

type enterPlanModeTool struct{}

func newEnterPlanModeTool() *enterPlanModeTool { return &enterPlanModeTool{} }

func (t *enterPlanModeTool) Name() string  { return "enter_plan_mode" }
func (t *enterPlanModeTool) Label() string { return "Enter Plan Mode" }
func (t *enterPlanModeTool) Description() string {
	return "Enter plan mode for complex tasks that require careful planning before implementation. " +
		"In plan mode you can explore the codebase (read-only) and design an implementation plan. " +
		"Call this when a task needs architectural decisions, multiple valid approaches, or multi-file changes. " +
		"Do NOT call this for simple fixes, single-function additions, or tasks with very specific instructions."
}

func (t *enterPlanModeTool) Schema() map[string]any {
	return schema.Object()
}

func (t *enterPlanModeTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"status":  "entered_plan_mode",
		"message": "You are now in plan mode (read-only). Explore the codebase and call exit_plan_mode with your plan when ready.",
	})
}

// ---------------------------------------------------------------------------
// exit_plan_mode — LLM calls this to submit a plan and exit plan mode
// ---------------------------------------------------------------------------

type exitPlanModeTool struct{}

func newExitPlanModeTool() *exitPlanModeTool { return &exitPlanModeTool{} }

func (t *exitPlanModeTool) Name() string  { return "exit_plan_mode" }
func (t *exitPlanModeTool) Label() string { return "Exit Plan Mode" }
func (t *exitPlanModeTool) Description() string {
	return "Exit plan mode and submit your implementation plan with numbered steps. " +
		"Call this when you have finished exploring the codebase and have a clear plan. " +
		"The plan will be presented to the user for approval before execution."
}

func (t *exitPlanModeTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("steps", schema.Array(
			"The plan steps in execution order",
			schema.Object(
				schema.Property("description", schema.String("What this step does")).Required(),
			),
		)).Required(),
	)
}

type exitPlanModeArgs struct {
	Steps []struct {
		Description string `json:"description"`
	} `json:"steps"`
}

func (t *exitPlanModeTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a exitPlanModeArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if len(a.Steps) == 0 {
		return nil, fmt.Errorf("steps must not be empty")
	}
	for i, s := range a.Steps {
		if s.Description == "" {
			return nil, fmt.Errorf("step %d has empty description", i+1)
		}
	}
	return json.Marshal(map[string]any{
		"steps":   a.Steps,
		"count":   len(a.Steps),
		"message": fmt.Sprintf("Plan submitted with %d steps.", len(a.Steps)),
	})
}

// ---------------------------------------------------------------------------
// mark_step — LLM calls this to mark a plan step as completed
// ---------------------------------------------------------------------------

type markStepTool struct{}

func newMarkStepTool() *markStepTool { return &markStepTool{} }

func (t *markStepTool) Name() string        { return "mark_step" }
func (t *markStepTool) Label() string        { return "Mark Step Done" }
func (t *markStepTool) Description() string  { return "Mark a plan step as completed. Call this after finishing each step of the plan." }

func (t *markStepTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("step", schema.Int("Step number to mark as completed (1-based)")).Required(),
	)
}

type markStepArgs struct {
	Step int `json:"step"`
}

func (t *markStepTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a markStepArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Step <= 0 {
		return nil, fmt.Errorf("step must be a positive integer")
	}
	return json.Marshal(map[string]any{
		"step":    a.Step,
		"message": fmt.Sprintf("Step %d marked as completed.", a.Step),
	})
}
