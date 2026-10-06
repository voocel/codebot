package app

import (
	"context"
	"encoding/json"
	"log"

	"github.com/voocel/agentcore"
	coresub "github.com/voocel/agentcore/subagent"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/subagent"
	"github.com/voocel/codebot/internal/agent/tools"
)

// buildTools builds the conversation's own tools for the current model. MCP
// tools are added per run, see spec. Callers hold c.mu.
func (c *Conversation) buildTools() []agentcore.Tool {
	a := c.app
	ws := agentcoretools.Workspace{Dir: c.cwd, FS: a.opts.FS, Files: c.files, Tasks: c.tasks}

	// Sub-agents get every tool up to here, each re-pooled per agent.
	out := append(ws.Tools(),
		tools.NewWebFetch(a.settings.SearchProvider, a.settings.SearchAPIKey),
		tools.NewWebSearch(a.settings.SearchProvider, a.settings.SearchAPIKey),
		tools.NewTodoWrite(),
	)
	if a.opts.Interactive {
		out = append(out, tools.NewAskUser(a.opts.UI))
	}

	c.subagents = c.buildSubagents(out, ws)
	out = append(out,
		c.subagents,
		tools.NewSkillTool(c.activeSkills, c.id, c.subagents.Run, c.skillInvoked),
		// Main agent only, so after the sub-agents' pool.
		tools.NewEnterWorktree(c.EnterWorktree),
		tools.NewExitWorktree(func(discard bool) (string, error) {
			res, err := c.ExitWorktree(discard)
			if err != nil {
				return "", err
			}
			return res.forModel(), nil
		}),
	)
	return append(out, c.tasks.Tools()...)
}

// buildSubagents builds the subagent tool over the given tools.
func (c *Conversation) buildSubagents(pool []agentcore.Tool, ws agentcoretools.Workspace) agentcore.Tool {
	a, model := c.app, c.model
	deps := subagent.BuildDeps{
		Workspace:    ws,
		MainTools:    pool,
		DefaultModel: model.model,
		ResolveModel: func(name string) (agentcore.Model, error) { return a.resolveModelName(model.provider, name) },
		CompactAt:    model.compactAt,
		Retry:        retryPolicy,
		SessionID:    c.id,
		// A sub-agent runs its own loop: the main loop's middleware does not
		// reach it.
		Middleware: c.middleware(),
		Emit:       agentEmit(c.agents),
	}
	var agents []coresub.Agent
	for _, def := range subagent.Definitions(c.cwd, model.small, a.Extensions().Agents) {
		agent, err := def.Agent(deps)
		if err != nil {
			log.Printf("subagent %q: skipped (%v)", def.Name, err)
			continue
		}
		agents = append(agents, agent)
	}
	return coresub.New(c.tasks, agents...)
}

// forkSkill runs a forked skill in a sub-agent.
func (c *Conversation) forkSkill(ctx context.Context, args json.RawMessage) (agentcore.Result, error) {
	c.mu.Lock()
	agents := c.subagents
	c.mu.Unlock()
	return agents.Run(ctx, args)
}

// coreToolNames stay visible when the model supports tool search; the rest
// load on demand. Frequently used tools stay so the first turn needs no
// search round-trip.
var coreToolNames = map[string]bool{
	"read":       true,
	"write":      true,
	"edit":       true,
	"bash":       true,
	"grep":       true,
	"glob":       true,
	"ls":         true,
	"todo_write": true,
	"ask_user":   true,
}

// withToolSearch defers the non-core tools behind tool_search when the
// model's adapter defers tools, as Anthropic's does: the models it serves are
// taken to be Claude 4.5 or later, which take deferred tools. Elsewhere a
// tool search would add each tool it finds to the request mid-session, which
// restarts the prompt cache and invalidates Claude's thinking.
func withToolSearch(all []agentcore.Tool, client *litellm.Client) []agentcore.Tool {
	if caps, _ := client.Capabilities(); !caps.DeferredTools {
		return all
	}
	var visible, deferred []agentcore.Tool
	for _, t := range all {
		if coreToolNames[t.Name] {
			visible = append(visible, t)
		} else {
			deferred = append(deferred, t)
		}
	}
	if len(deferred) == 0 {
		return all
	}
	return append(visible, agentcoretools.Defer(deferred)...)
}
