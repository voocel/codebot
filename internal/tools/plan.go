package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/voocel/agentcore/permission"
	"github.com/voocel/agentcore/schema"
)

// AllowedPromptArg is a semantic label the model may attach to exit_plan_mode
// to remind the user about follow-up actions (e.g. {Tool: "Bash", Prompt: "go test"}).
// These are reference-only — they do NOT auto-allow tool calls. The approval
// engine surfaces them in the plan-exit prompt preview alongside the plan body.
type AllowedPromptArg struct {
	Tool   string `json:"tool"`
	Prompt string `json:"prompt"`
}

// ---------------------------------------------------------------------------
// enter_plan_mode
// ---------------------------------------------------------------------------

type EnterPlanModeTool struct {
	validate func() error
	onEnter  func() (string, error)
}

func NewEnterPlanMode() *EnterPlanModeTool { return &EnterPlanModeTool{} }

func (t *EnterPlanModeTool) SetValidator(fn func() error) { t.validate = fn }
func (t *EnterPlanModeTool) SetHandler(fn func() (string, error)) {
	t.onEnter = fn
}

func (t *EnterPlanModeTool) Name() string  { return "enter_plan_mode" }
func (t *EnterPlanModeTool) Label() string { return "Enter Plan Mode" }
func (t *EnterPlanModeTool) PermissionMetadata() permission.Metadata {
	return permission.Metadata{Capability: permission.CapabilityInternal}
}
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
	return schema.Object()
}

func (t *EnterPlanModeTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	if t.validate != nil {
		if err := t.validate(); err != nil {
			return nil, err
		}
	}
	message := "You are now in plan mode. Build your plan in the plan file referenced by the plan-mode system message, then call exit_plan_mode when ready."
	if t.onEnter != nil {
		prompt, err := t.onEnter()
		if err != nil {
			return nil, err
		}
		message = prompt
	}
	return json.Marshal(map[string]any{
		"status":  "entered",
		"message": message,
	})
}

// ---------------------------------------------------------------------------
// exit_plan_mode
// ---------------------------------------------------------------------------

type ExitPlanModeTool struct {
	validate func() error
	exiter   func() (string, error)
}

func NewExitPlanMode() *ExitPlanModeTool { return &ExitPlanModeTool{} }

func (t *ExitPlanModeTool) SetValidator(fn func() error) { t.validate = fn }

// SetExiter wires the plan-mode state transition. Called only after the
// permission engine has approved the exit (CC-style: ExitPlanMode declares
// checkPermissions:'ask', so the user has already seen the plan and chosen
// approve before the tool runs). The callback returns the approved plan
// content so it can be echoed in the tool result for the model.
func (t *ExitPlanModeTool) SetExiter(fn func() (string, error)) {
	t.exiter = fn
}

func (t *ExitPlanModeTool) Name() string  { return "exit_plan_mode" }
func (t *ExitPlanModeTool) Label() string { return "Exit Plan Mode" }
func (t *ExitPlanModeTool) PermissionMetadata() permission.Metadata {
	return permission.Metadata{Capability: permission.CapabilityInternal}
}
func (t *ExitPlanModeTool) Description() string {
	return "Signal that the plan file (referenced in the plan-mode system message) is ready for user review. " +
		"You should already have written the plan to that file using write or edit. " +
		"This tool reads the plan from disk and asks the user to approve or deny; " +
		"do NOT pass the plan content as an argument. " +
		"Optional allowed_prompts annotate follow-up actions for the user; they are reference labels and do NOT auto-allow anything."
}

func (t *ExitPlanModeTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("allowed_prompts", schema.Array(
			"Optional reference labels for follow-up actions the user may want to take after approving the plan. Each item is {tool, prompt}, e.g. {tool: \"Bash\", prompt: \"go test ./...\"}.",
			schema.Object(
				schema.Property("tool", schema.String("Tool name, e.g. \"Bash\"")).Required(),
				schema.Property("prompt", schema.String("Short label for the follow-up action")).Required(),
			),
		)),
	)
}

func (t *ExitPlanModeTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	if t.validate != nil {
		if err := t.validate(); err != nil {
			return nil, err
		}
	}
	if t.exiter == nil {
		return nil, ErrPlanExitNotWired
	}
	// Approval has already happened upstream in approval.Engine.Decide; the
	// permission engine wouldn't have invoked Execute on a denial. Here we
	// just transition state and return the approved plan content.
	content, err := t.exiter()
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"status":  "approved",
		"plan":    content,
		"message": "User approved the plan. Continue executing it now.",
	})
}

// ErrPlanExitNotWired is returned when exit_plan_mode is invoked before the
// harness wires the exiter callback. Indicates a bootstrapping bug.
var ErrPlanExitNotWired = errors.New("exit_plan_mode is not wired")
