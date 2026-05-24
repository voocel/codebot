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
	return `Use this tool proactively when you're about to start a non-trivial implementation task. Getting user sign-off on your approach before writing code prevents wasted effort and ensures alignment. This tool transitions you into plan mode where you can explore the codebase and design an implementation approach for user approval.

## When to Use This Tool

**Prefer using enter_plan_mode** for implementation tasks unless they're simple. Use it when ANY of these conditions apply:

1. **New Feature Implementation**: Adding meaningful new functionality
   - Example: "Add a logout button" - where should it go? What should happen on click?
   - Example: "Add form validation" - what rules? What error messages?

2. **Multiple Valid Approaches**: The task can be solved in several different ways
   - Example: "Add caching to the API" - could use Redis, in-memory, file-based, etc.
   - Example: "Improve performance" - many optimization strategies possible

3. **Code Modifications**: Changes that affect existing behavior or structure
   - Example: "Update the login flow" - what exactly should change?
   - Example: "Refactor this component" - what's the target architecture?

4. **Architectural Decisions**: The task requires choosing between patterns or technologies
   - Example: "Add real-time updates" - WebSockets vs SSE vs polling
   - Example: "Implement state management" - centralized store vs context vs custom

5. **Multi-File Changes**: The task will likely touch more than 2-3 files
   - Example: "Refactor the authentication system"
   - Example: "Add a new API endpoint with tests"

6. **Unclear Requirements**: You need to explore before understanding the full scope
   - Example: "Make the app faster" - need to profile and identify bottlenecks
   - Example: "Fix the bug in checkout" - need to investigate root cause

7. **User Preferences Matter**: The implementation could reasonably go multiple ways
   - If you would use ask_user to clarify the approach, use enter_plan_mode instead
   - Plan mode lets you explore first, then present options with context

## When NOT to Use This Tool

Only skip enter_plan_mode for simple tasks:
- Single-line or few-line fixes (typos, obvious bugs, small tweaks)
- Adding a single function with clear requirements
- Tasks where the user has given very specific, detailed instructions
- Pure research/exploration tasks

## Examples

### GOOD - Use enter_plan_mode:
User: "Add user authentication to the app"
- Requires architectural decisions (session vs JWT, where to store tokens, middleware structure)

User: "Optimize the database queries"
- Multiple approaches possible, need to profile first, significant impact

User: "Implement dark mode"
- Architectural decision on theme system, affects many components

User: "Add a delete button to the user profile"
- Seems simple but involves: where to place it, confirmation dialog, API call, error handling, state updates

User: "Update the error handling in the API"
- Affects multiple files, user should approve the approach

### BAD - Don't use enter_plan_mode:
User: "Fix the typo in the README"
- Straightforward, no planning needed

User: "Add a console.log to debug this function"
- Simple, obvious implementation

User: "What files handle routing?"
- Research task, not implementation planning

## Important Notes

- If unsure whether to use it, err on the side of planning - it's better to get alignment upfront than to redo work
- Users appreciate being consulted before significant changes are made to their codebase
- When in doubt for a *specific* question (e.g. "which library?", "which auth method?"), prefer starting work and asking with ask_user over entering a full planning phase. Plan mode is for shaping a whole approach, not for resolving one decision.`
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
// permission engine has approved the exit — ExitPlanMode is an ask-style
// tool, so the user has already seen the plan and chosen approve before the
// tool runs. The callback returns the approved plan content so it can be
// echoed in the tool result for the model.
func (t *ExitPlanModeTool) SetExiter(fn func() (string, error)) {
	t.exiter = fn
}

func (t *ExitPlanModeTool) Name() string  { return "exit_plan_mode" }
func (t *ExitPlanModeTool) Label() string { return "Exit Plan Mode" }
func (t *ExitPlanModeTool) PermissionMetadata() permission.Metadata {
	return permission.Metadata{Capability: permission.CapabilityInternal}
}
func (t *ExitPlanModeTool) Description() string {
	return `Use this tool when you are in plan mode and have finished writing your plan to the plan file and are ready for user approval.

## How This Tool Works
- You should have already written your plan to the plan file specified in the plan-mode system message
- This tool does NOT take the plan content as a parameter — it reads the plan from the file you wrote
- This tool simply signals that you're done planning and ready for the user to review and approve
- The user will see the contents of your plan file when they review it

## When to Use This Tool
IMPORTANT: Only use this tool when the task requires planning the implementation steps of a task that requires writing code. For research tasks where you're gathering information, searching files, reading files or in general trying to understand the codebase — do NOT use this tool.

## Before Using This Tool
Ensure your plan is complete and unambiguous:
- If you have unresolved questions about requirements or approach, use ask_user first (in earlier phases)
- Once your plan is finalized, use THIS tool to request approval

**Important:** Do NOT use ask_user to ask "Is this plan okay?" or "Should I proceed?" — that's exactly what THIS tool does. exit_plan_mode inherently requests user approval of your plan.

## Examples

1. Initial task: "Search for and understand the implementation of vim mode in the codebase" — Do not use exit_plan_mode because you are not planning the implementation steps of a task.
2. Initial task: "Help me implement yank mode for vim" — Use exit_plan_mode after you have finished planning the implementation steps of the task.
3. Initial task: "Add a new feature to handle user authentication" — If unsure about auth method (OAuth, JWT, etc.), use ask_user first, then use exit_plan_mode after clarifying the approach.

## Optional: allowed_prompts
You may attach reference labels for follow-up actions the user might run after approving the plan (e.g. {tool: "Bash", prompt: "go test ./..."}). These are reference-only — they do NOT auto-allow tool calls. The approval engine surfaces them in the plan-exit prompt preview alongside the plan body.`
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
