package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"

	"github.com/voocel/codebot/internal/agent/skill"
)

// ForkExecutor runs a task in a forked subagent context: the subagent
// tool's Run, so it can be wired directly.
type ForkExecutor func(ctx context.Context, args json.RawMessage) (agentcore.Result, error)

// NewSkillTool returns the skill tool, which lets the model invoke skills by
// name: it loads the skill, expands its $ARGUMENTS and returns the prompt.
// Skills with context: fork run in a subagent through fork. catalog returns
// the skills active in the conversation's workspace; invoked sees every
// invocation before it runs.
func NewSkillTool(catalog func() *skill.Catalog, sessionID string, fork ForkExecutor, invoked func(*skill.Invocation)) agentcore.Tool {
	tool := agentcore.NewTool("skill", skillDescription,
		schema.Object(
			schema.Property("skill", schema.String("The skill name, e.g. \"commit\", \"review-pr\"")).Required(),
			schema.Property("args", schema.String("Optional arguments for the skill")),
		),
		func(ctx context.Context, a skillArgs) (agentcore.Result, error) {
			name := strings.ToLower(strings.TrimSpace(a.Skill))
			if name == "" {
				return agentcore.Result{}, errors.New("skill name is required")
			}
			inv, err := catalog().Invoke(ctx, skill.InvokeInput{
				Name:      name,
				Args:      a.Args,
				SessionID: sessionID,
				By:        skill.ByModel,
			})
			switch {
			case errors.Is(err, skill.ErrNotFound):
				return agentcore.TextResult(fmt.Sprintf(
					"Skill %q not found. Check the skills listed in the system prompt.", name)), nil
			case errors.Is(err, skill.ErrModelInvocationDenied):
				return agentcore.TextResult(fmt.Sprintf(
					"Skill %q is configured for manual invocation only. The user can invoke it with /%s.", name, name)), nil
			case err != nil:
				return agentcore.Result{}, err
			}
			invoked(inv)
			if inv.Fork {
				return ForkSkill(ctx, inv, fork)
			}
			return agentcore.TextResult(inv.Prompt), nil
		},
	)
	tool.Label = "Skill"
	return tool
}

const skillDescription = `Execute a skill within the conversation.

Available skills are listed in the system prompt under "Skills". When a user's task matches a skill description, or when they reference a skill by name (e.g. "/commit"), invoke this tool with the skill name and optional arguments.

How to invoke:
- skill: "commit" — invoke the commit skill
- skill: "commit", args: "-m 'Fix bug'" — invoke with arguments
- skill: "review-pr", args: "123" — invoke with arguments

Important:
- When a matching skill exists, this is a BLOCKING REQUIREMENT: invoke this tool BEFORE generating any other response about the task
- NEVER mention a skill without actually calling this tool
- Only invoke listed skills; do not guess skill names
- Do not invoke a skill that is already running
- Do not use this tool for built-in commands (/help, /clear, /model, etc.)
- If you see a <skill> tag in the current conversation turn, the skill has ALREADY been loaded — follow the instructions directly instead of calling this tool again`

type skillArgs struct {
	Skill string `json:"skill"`
	Args  string `json:"args"`
}

// ForkSkill runs a forked skill through fork, returning the sub-agent's
// output.
func ForkSkill(ctx context.Context, inv *skill.Invocation, fork ForkExecutor) (agentcore.Result, error) {
	params := map[string]string{"agent": inv.Agent, "task": inv.Prompt}
	if inv.Model != "" {
		params["model"] = inv.Model
	}
	args, err := json.Marshal(params)
	if err != nil {
		return agentcore.Result{}, err
	}
	return fork(ctx, args)
}
