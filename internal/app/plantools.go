package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
)

// submit_plan — LLM calls this to signal that the plan is ready for review.
// The plan content is the free-form text the LLM already wrote.
// This tool only captures a short title for display/persistence.

type submitPlanTool struct{}

func newSubmitPlanTool() *submitPlanTool { return &submitPlanTool{} }

func (t *submitPlanTool) Name() string  { return "submit_plan" }
func (t *submitPlanTool) Label() string { return "Submit Plan" }
func (t *submitPlanTool) Description() string {
	return "Signal that the implementation plan is ready for user review. " +
		"Call this after you have explored the codebase and written your plan as text. " +
		"The plan content is what you already wrote — this tool just signals completion."
}

func (t *submitPlanTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("title", schema.String("Short title summarizing the plan (under 60 chars)")).Required(),
	)
}

func (t *submitPlanTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if a.Title == "" {
		return nil, fmt.Errorf("title must not be empty")
	}
	return json.Marshal(map[string]any{
		"status":  "submitted",
		"title":   a.Title,
		"message": "Plan submitted for review. Waiting for user approval.",
	})
}
