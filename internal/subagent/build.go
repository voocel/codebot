package subagent

import (
	"slices"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/compact"
	coresub "github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/provider"
)

// BuildDeps is what Agent needs from the conversation, shared by every
// definition.
type BuildDeps struct {
	// Workspace is the parent's, which the sub-agents' read, write and edit
	// work in, with a record of reads of their own.
	Workspace tools.Workspace

	// MainTools are the main agent's tools the sub-agents draw on. They are
	// built before the subagent tool itself, so a sub-agent cannot spawn
	// another.
	MainTools []agentcore.Tool

	// DefaultModel runs the agents whose definition names no model.
	DefaultModel agentcore.Model

	// ResolveModel resolves a model a definition or a call names.
	ResolveModel func(name string) (agentcore.Model, error)

	// CompactAt is the token estimate above which a sub-agent run compacts
	// its history, matching the parent's threshold.
	CompactAt int

	// MaxRetries is how often a sub-agent retries a model call that failed
	// transiently, as the parent does.
	MaxRetries int

	// SessionID is the parent session's identity, the base of each run's
	// prompt-cache routing key.
	SessionID string

	// Middleware wraps every tool call inside the sub-agents: a sub-agent
	// runs its own loop and inherits none from the parent.
	Middleware []agentcore.ToolMiddleware

	// Emit, if set, returns where the events of a run go, nil for nowhere.
	Emit func(coresub.Spawn) func(agentcore.Event) error
}

// Agent makes the sub-agent of a definition. Each run gets tools of its own,
// see toolPool, and a prompt-cache key of its own: one conversation, one key.
func (d *AgentDefinition) Agent(deps BuildDeps) (coresub.Agent, error) {
	model := deps.DefaultModel
	if d.Model != "" && d.Model != "inherit" {
		var err error
		if model, err = deps.ResolveModel(d.Model); err != nil {
			return coresub.Agent{}, err
		}
	}
	return coresub.Agent{
		Name:        d.Name,
		Description: d.Description,
		Config: func(s coresub.Spawn) (agentcore.Config, error) {
			m := model
			if s.Model != "" {
				var err error
				if m, err = deps.ResolveModel(s.Model); err != nil {
					return agentcore.Config{}, err
				}
			}
			cfg := agentcore.Config{
				Model:      provider.WithCacheKey(m, deps.SessionID+"-"+s.ID),
				System:     []litellm.Block{litellm.Text(d.SystemPrompt)},
				Tools:      toolPool(deps, d),
				Middleware: deps.Middleware,
				MaxTurns:   d.MaxTurns,
				MaxRetries: deps.MaxRetries,
				// A breakpoint on the freshest message, so each call of a
				// tool loop reads the one before from the cache.
				Cache: &litellm.CacheControl{},
			}
			if deps.CompactAt > 0 {
				cfg.Compactor, cfg.CompactAt = compact.Summarizer{}, deps.CompactAt
			}
			if deps.Emit != nil {
				cfg.Emit = deps.Emit(s)
			}
			return cfg, nil
		},
	}, nil
}

// mainAgentOnly are tools no sub-agent gets: ask_user, since the main agent
// owns the dialogue with the user, and todo_write, since the checklist
// belongs to the main conversation (a sub-agent's would live in its own
// history, where nobody sees it).
var mainAgentOnly = []string{"ask_user", "todo_write"}

// toolPool returns a sub-agent's tools: the main agent's, less mainAgentOnly
// and the definition's disallowed ones, narrowed to its tools list. Read,
// write and edit are rebuilt over a FileReadState of the run's own, so its
// reads cannot vouch for the parent's writes, nor another run's.
func toolPool(deps BuildDeps, d *AgentDefinition) []agentcore.Tool {
	narrowed := len(d.Tools) > 0 && !slices.Contains(d.Tools, "*")
	w := deps.Workspace
	w.Files = tools.NewFileReadState()
	var out []agentcore.Tool
	for _, t := range deps.MainTools {
		if slices.Contains(mainAgentOnly, t.Name) || slices.Contains(d.DisallowedTools, t.Name) ||
			narrowed && !slices.Contains(d.Tools, t.Name) {
			continue
		}
		switch t.Name {
		case "read":
			t = w.Read()
		case "write":
			t = w.Write()
		case "edit":
			t = w.Edit()
		}
		out = append(out, t)
	}
	return out
}
