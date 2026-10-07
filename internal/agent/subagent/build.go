package subagent

import (
	"slices"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/compact"
	coresub "github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/retry"

	"github.com/voocel/codebot/internal/infra/provider"
)

type BuildDeps struct {
	// Workspace is the parent's; each run gets its own read state (see
	// toolPool).
	Workspace tools.Workspace

	// MainTools excludes the subagent tool, so a sub-agent can't spawn
	// another.
	MainTools []agentcore.Tool

	DefaultModel agentcore.Model
	ResolveModel func(name string) (agentcore.Model, error)

	// CompactAt and Retry match the parent's.
	CompactAt int
	Retry     retry.Policy

	// SessionID prefixes each run's prompt-cache key.
	SessionID string

	// Middleware wraps the sub-agents' tool calls; a sub-agent's loop
	// inherits none from the parent.
	Middleware []agentcore.ToolMiddleware

	// Emit may return nil to drop a run's events.
	Emit func(coresub.Spawn) func(agentcore.Event) error
}

// Agent gives each run its own tools (see toolPool) and its own prompt-cache
// key.
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
				Model: provider.WithCacheKey(m, deps.SessionID+"-"+s.ID),
				// Cache the system prompt so spawns of one agent share it.
				// The default TTL suffices: a spawn's calls don't wait on a
				// person.
				System:     []litellm.Block{litellm.TextBlock{Text: d.SystemPrompt, Cache: &litellm.CacheControl{}}},
				Tools:      toolPool(deps, d),
				Middleware: deps.Middleware,
				MaxTurns:   d.MaxTurns,
				Retry:      deps.Retry,
				// Cache the latest message and where the previous call
				// ended, so each call reuses the one before.
				Cache:     &litellm.CacheControl{},
				Compactor: compact.Summarizer{Notes: tools.FileOps},
				CompactAt: deps.CompactAt,
				Emit:      deps.Emit(s),
			}
			return cfg, nil
		},
	}, nil
}

// mainAgentOnly: the main agent owns the dialogue with the user, and a
// sub-agent's todo list would sit in a history nobody sees.
var mainAgentOnly = []string{"ask_user", "todo_write"}

// toolPool rebuilds read, write and edit over the run's own read state, so
// its reads can't vouch for writes by the parent or another run.
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
