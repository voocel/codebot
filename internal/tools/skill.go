package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/codebot/internal/skill"
)

// ForkExecutor runs a task in a forked subagent context.
// It matches the SubAgentTool.Execute signature so it can be wired directly.
type ForkExecutor func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)

// AllowToolsSetter grants temporary tool permissions for the active skill.
// Called with nil/empty to clear grants from the previous skill.
type AllowToolsSetter func(tools []string)
type InvocationApplier func(result *skill.InvocationResult) error

type SkillExecutionResult struct {
	PromptText string
	Forked     bool
	ForkOutput json.RawMessage
}

// SkillTool lets the LLM invoke skills by name.
// It loads the skill file, strips frontmatter, expands $ARGUMENTS placeholders,
// and returns the formatted content as a tool result.
// Skills with context: fork are delegated to a subagent via the ForkExecutor.
type SkillTool struct {
	mu               sync.RWMutex
	catalog          *skill.Catalog
	sessionID        string
	forkExecutor     ForkExecutor
	allowToolsSetter AllowToolsSetter
	applyInvocation  InvocationApplier
}

// NewSkillTool creates a SkillTool with the given initial skill list.
func NewSkillTool(catalog *skill.Catalog, sessionID string) *SkillTool {
	return &SkillTool{catalog: catalog, sessionID: sessionID}
}

// SetCatalog replaces the active skill catalog (called on /reload).
func (t *SkillTool) SetCatalog(catalog *skill.Catalog) {
	t.mu.Lock()
	t.catalog = catalog
	t.mu.Unlock()
}

// SetForkExecutor sets the function used to run context: fork skills in a subagent.
func (t *SkillTool) SetForkExecutor(fn ForkExecutor) {
	t.forkExecutor = fn
}

// SetAllowToolsSetter sets the function used to grant temporary tool permissions.
func (t *SkillTool) SetAllowToolsSetter(fn AllowToolsSetter) {
	t.allowToolsSetter = fn
}

func (t *SkillTool) SetInvocationApplier(fn InvocationApplier) {
	t.applyInvocation = fn
}

func (t *SkillTool) Name() string  { return "Skill" }
func (t *SkillTool) Label() string { return "Skill" }

func (t *SkillTool) Description() string {
	return `Execute a skill within the conversation.

Available skills are listed in system-reminder messages. When a user's task matches a skill description, or when they reference a skill by name (e.g. "/commit"), invoke this tool with the skill name and optional arguments.

How to invoke:
- skill: "commit" — invoke the commit skill
- skill: "commit", args: "-m 'Fix bug'" — invoke with arguments
- skill: "review-pr", args: "123" — invoke with arguments

Important:
- When a matching skill exists, this is a BLOCKING REQUIREMENT: invoke this tool BEFORE generating any other response about the task
- NEVER mention a skill without actually calling this tool
- Only invoke skills listed in system reminders; do NOT guess skill names
- Do not invoke a skill that is already running
- Do not use this tool for built-in commands (/help, /clear, /model, etc.)
- If you see a <skill> tag in the current conversation turn, the skill has ALREADY been loaded — follow the instructions directly instead of calling this tool again`
}

func (t *SkillTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("skill", schema.String("The skill name, e.g. \"commit\", \"review-pr\"")).Required(),
		schema.Property("args", schema.String("Optional arguments for the skill")),
	)
}

type skillArgs struct {
	Skill string `json:"skill"`
	Args  string `json:"args"`
}

func ExecuteSkillInvocation(ctx context.Context, result *skill.InvocationResult, apply InvocationApplier, fork ForkExecutor) (*SkillExecutionResult, error) {
	if result == nil {
		return nil, fmt.Errorf("skill invocation result is nil")
	}
	if apply != nil {
		if err := apply(result); err != nil {
			return nil, err
		}
	}
	if result.Mode != skill.ModeFork {
		return &SkillExecutionResult{PromptText: result.PromptText}, nil
	}
	if fork == nil {
		return nil, fmt.Errorf("forked skill %q requires a fork executor", result.Spec.Name)
	}
	forkArgs, err := BuildSkillForkArgs(result)
	if err != nil {
		return nil, err
	}
	out, err := fork(ctx, forkArgs)
	if err != nil {
		return nil, err
	}
	return &SkillExecutionResult{
		Forked:     true,
		ForkOutput: out,
	}, nil
}

func BuildSkillForkArgs(result *skill.InvocationResult) (json.RawMessage, error) {
	if result == nil {
		return nil, fmt.Errorf("skill invocation result is nil")
	}
	params := map[string]string{
		"agent": result.Agent,
		"task":  result.PromptText,
	}
	if result.Delta.ModelOverride != "" {
		params["model"] = result.Delta.ModelOverride
	}
	return json.Marshal(params)
}

func (t *SkillTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a skillArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	name := strings.ToLower(strings.TrimSpace(a.Skill))
	if name == "" {
		return nil, fmt.Errorf("skill name is required")
	}

	t.mu.RLock()
	catalog := t.catalog
	sessionID := t.sessionID
	forkExecutor := t.forkExecutor
	allowSetter := t.allowToolsSetter
	applyInvocation := t.applyInvocation
	t.mu.RUnlock()

	result, err := skill.ProcessInvocation(ctx, catalog, skill.InvokeInput{
		Name:      name,
		Args:      a.Args,
		SessionID: sessionID,
		Source:    skill.SourceModel,
	})
	if err == skill.ErrNotFound {
		return json.Marshal(fmt.Sprintf(
			"Skill %q not found. Check available skills in system-reminder messages.", name))
	}
	if err == skill.ErrModelInvocationDenied {
		return json.Marshal(fmt.Sprintf(
			"Skill %q is configured for manual invocation only. The user can invoke it with /%s.", name, name))
	}
	if err != nil {
		return nil, err
	}

	execApply := applyInvocation
	if execApply == nil && allowSetter != nil {
		execApply = func(result *skill.InvocationResult) error {
			if result != nil && result.Mode != skill.ModeFork {
				allowSetter(result.Delta.AllowedTools)
			}
			return nil
		}
	}

	execResult, err := ExecuteSkillInvocation(ctx, result, execApply, forkExecutor)
	if err != nil {
		return nil, err
	}
	if execResult.Forked {
		return execResult.ForkOutput, nil
	}
	return json.Marshal(execResult.PromptText)
}
