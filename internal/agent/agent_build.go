package agent

import (
	"fmt"
	"slices"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/subagent"
)

// BuildDeps carries the runtime context BuildConfig needs to materialise an
// AgentDefinition into a subagent.Config. It is constructed once per call to
// buildSubAgentTool and reused across every definition.
//
// Why a struct instead of positional args: BuildConfig is called from
// bootstrap, and bootstrap already has eight parameters threading through
// subagent assembly. A struct lets us add fields (e.g. eventual MCP server
// inheritance) without breaking call sites.
type BuildDeps struct {
	// Cwd is the workspace root. Used by sub-agent prompts and by the
	// per-agent tool pool when constructing scoped read/write/edit
	// instances.
	Cwd string

	// MainTools is the parent agent's tool list AS HANDED TO buildSubAgentTool
	// — that is, before the subagent and Skill tools are appended. Every
	// AgentDefinition gets this list re-pooled into independent instances
	// inside BuildConfig (see BuildToolPool's contract).
	MainTools []agentcore.Tool

	// DefaultModel is the parent agent's chat model, used when the
	// AgentDefinition either omits Model or sets it to "inherit".
	DefaultModel agentcore.ChatModel

	// ContextWindow is the parent's context window size, threaded to the
	// per-agent ContextManager so summary thresholds align with the model
	// in use.
	ContextWindow int

	// ResolveModel maps an explicit model name from AgentDefinition.Model
	// to a ChatModel. May be nil when the build pipeline doesn't support
	// per-agent model override; in that case any explicit Model that
	// isn't "inherit" produces an error from BuildConfig.
	ResolveModel func(name string) (agentcore.ChatModel, error)
}

// BuildConfig converts an AgentDefinition into a subagent.Config ready to be
// passed to subagent.New(...). It is responsible for:
//
//   - Resolving the model name to a ChatModel (inherit vs explicit).
//   - Calling BuildToolPool to give the sub-agent its own read/write/edit
//     instances (independent FileReadState — see Stage 2 review).
//   - Running FilterToolsForAgent with rules derived from Source/Background.
//   - Applying AgentDefinition.Tools as a post-filter allow-list when set.
//   - Wiring a fresh ContextManager via the factory the caller supplied.
//
// BuildConfig is pure with respect to BuildDeps: calling it twice with the
// same definition and deps yields independent subagent.Config values (which
// is what the per-agent FileReadState invariant requires).
func (d *AgentDefinition) BuildConfig(deps BuildDeps, ctxFactory func(agentcore.ChatModel) agentcore.ContextManager) (subagent.Config, error) {
	if err := d.Validate(); err != nil {
		return subagent.Config{}, err
	}

	model, err := resolveModel(d.Model, deps)
	if err != nil {
		return subagent.Config{}, fmt.Errorf("agent %q: %w", d.Name, err)
	}

	pool := BuildToolPool(deps.Cwd, deps.MainTools)
	filtered := FilterToolsForAgent(pool, FilterOpts{
		IsBuiltIn:       d.Source.IsBuiltIn(),
		IsAsync:         d.Background,
		AllowMCP:        true,
		ExtraDisallowed: d.DisallowedTools,
	})
	tools := applyAllowList(filtered, d.Tools)

	cfg := subagent.Config{
		Name:                  d.Name,
		Description:           d.Description,
		Model:                 model,
		SystemPrompt:          d.SystemPrompt,
		Tools:                 tools,
		MaxTurns:              d.MaxTurns,
		ContextManagerFactory: ctxFactory,
		ConvertToLLM:          agentctx.ContextConvertToLLM,
	}
	return cfg, nil
}

// resolveModel maps AgentDefinition.Model to a ChatModel.
//
//	""        → DefaultModel (the parent's model)
//	"inherit" → DefaultModel
//	<name>    → deps.ResolveModel(name)
//
// The "inherit" alias is accepted for ergonomics — frontmatter authors often
// write it out explicitly even though it's the default; both empty and
// "inherit" mean the same thing.
func resolveModel(name string, deps BuildDeps) (agentcore.ChatModel, error) {
	if name == "" || name == "inherit" {
		if deps.DefaultModel == nil {
			return nil, fmt.Errorf("no default model available")
		}
		return deps.DefaultModel, nil
	}
	if deps.ResolveModel == nil {
		return nil, fmt.Errorf("model %q requested but no resolver wired", name)
	}
	return deps.ResolveModel(name)
}

// applyAllowList narrows `filtered` to those tools named in `allow`. An empty
// or {"*"} allow list means "no further narrowing" — the global filter
// output passes through untouched. Unknown names in the allow list are
// silently dropped because the filter may already have removed them (e.g. an
// agent says `tools: [bash]` but its trust tier denies bash); silent drop is
// a softer failure mode than erroring at boot, and the user can introspect
// the effective pool via `/agents show <name>` when that lands.
func applyAllowList(filtered []agentcore.Tool, allow []string) []agentcore.Tool {
	if len(allow) == 0 {
		return filtered
	}
	if len(allow) == 1 && allow[0] == "*" {
		return filtered
	}
	out := make([]agentcore.Tool, 0, len(allow))
	for _, t := range filtered {
		if slices.Contains(allow, t.Name()) {
			out = append(out, t)
		}
	}
	return out
}
