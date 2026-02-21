package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
)

// ---------------------------------------------------------------------------
// submit_plan — LLM calls this to submit a structured plan
// ---------------------------------------------------------------------------

type submitPlanTool struct{}

func newSubmitPlanTool() *submitPlanTool { return &submitPlanTool{} }

func (t *submitPlanTool) Name() string        { return "submit_plan" }
func (t *submitPlanTool) Label() string        { return "Submit Plan" }
func (t *submitPlanTool) Description() string  { return "Submit an implementation plan with numbered steps. Call this tool when you have finished exploring the codebase and have a clear plan." }

func (t *submitPlanTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("steps", schema.Array(
			"The plan steps in execution order",
			schema.Object(
				schema.Property("description", schema.String("What this step does")).Required(),
			),
		)).Required(),
	)
}

type submitPlanArgs struct {
	Steps []struct {
		Description string `json:"description"`
	} `json:"steps"`
}

func (t *submitPlanTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a submitPlanArgs
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
		"steps": a.Steps,
		"count": len(a.Steps),
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
