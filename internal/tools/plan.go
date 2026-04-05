package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
)

// ---------------------------------------------------------------------------
// enter_plan_mode
// ---------------------------------------------------------------------------

type EnterPlanModeTool struct {
	validate func() error
}

func NewEnterPlanMode() *EnterPlanModeTool { return &EnterPlanModeTool{} }

func (t *EnterPlanModeTool) SetValidator(fn func() error) { t.validate = fn }

func (t *EnterPlanModeTool) Name() string  { return "enter_plan_mode" }
func (t *EnterPlanModeTool) Label() string { return "Enter Plan Mode" }
func (t *EnterPlanModeTool) Description() string {
	return `Enter plan mode to explore the codebase and design an implementation plan before making changes.

Use this PROACTIVELY when ANY of these apply:
- New feature or non-trivial functionality
- Multiple valid implementation approaches exist
- Changes affect existing behavior or structure
- Architectural decisions needed (patterns, technologies)
- Multi-file changes (more than 2-3 files)
- Unclear requirements or uncertain scope
- User explicitly asks to plan, think through, or design first

Do NOT use for:
- Single-line or few-line fixes (typos, obvious bugs)
- A single function with clear, specific requirements
- Pure research or information queries`
}

func (t *EnterPlanModeTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("task", schema.String("Brief description of the task to plan for")),
	)
}

func (t *EnterPlanModeTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	if t.validate != nil {
		if err := t.validate(); err != nil {
			return nil, err
		}
	}
	var a struct {
		Task string `json:"task"`
	}
	_ = json.Unmarshal(args, &a)
	return json.Marshal(map[string]any{
		"status":  "entered",
		"task":    a.Task,
		"message": "You are now in plan mode. Explore the codebase and write a detailed implementation plan. Write the full plan as text, then call exit_plan_mode with title, content, and any allowed command prefixes.",
	})
}

// ---------------------------------------------------------------------------
// exit_plan_mode
// ---------------------------------------------------------------------------

type ExitPlanModeTool struct {
	validate func() error
}

func NewExitPlanMode() *ExitPlanModeTool { return &ExitPlanModeTool{} }

func (t *ExitPlanModeTool) SetValidator(fn func() error) { t.validate = fn }

func (t *ExitPlanModeTool) Name() string  { return "exit_plan_mode" }
func (t *ExitPlanModeTool) Label() string { return "Exit Plan Mode" }
func (t *ExitPlanModeTool) Description() string {
	return "Submit your completed implementation plan for user review. " +
		"You MUST write the full plan as text in the conversation first so the user can see it, " +
		"then call this tool with the same content in the 'content' parameter."
}

func (t *ExitPlanModeTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("title", schema.String("Short title summarizing the plan (under 60 chars)")).Required(),
		schema.Property("content", schema.String("The full plan content (markdown). Must match the text you just wrote.")).Required(),
		schema.Property("allowed_commands", schema.Array(
			"Allowed command prefixes for follow-up execution. Use exact prefixes such as 'go test' or 'go mod tidy'. At most 5 items.",
			schema.Object(
				schema.Property("command_prefix", schema.String("Command prefix to allow, e.g. 'go test'")).Required(),
				schema.Property("description", schema.String("Short human-readable description of what this command does")),
			),
		)),
	)
}

func (t *ExitPlanModeTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	if t.validate != nil {
		if err := t.validate(); err != nil {
			return nil, err
		}
	}
	var a struct {
		Title           string `json:"title"`
		Content         string `json:"content"`
		AllowedCommands []struct {
			CommandPrefix string `json:"command_prefix"`
			Description   string `json:"description"`
		} `json:"allowed_commands"`
	}
	_ = json.Unmarshal(args, &a)
	if a.Title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if a.Content == "" {
		return nil, fmt.Errorf("content is required — write your plan first, then pass it here")
	}
	return json.Marshal(map[string]any{
		"status":           "submitted",
		"title":            a.Title,
		"content":          a.Content,
		"allowed_commands": a.AllowedCommands,
		"message":          "Plan submitted for review. Do not respond further.",
	})
}
