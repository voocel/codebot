package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/voocel/agentcore"
	coresub "github.com/voocel/agentcore/subagent"
	agentcoretools "github.com/voocel/agentcore/tools"

	"github.com/voocel/codebot/internal/subagent"
	"github.com/voocel/codebot/internal/tools"
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
		tools.NewSkillTool(a.skillCatalog, c.id, c.subagents.Run, c.skillInvoked),
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
		MaxRetries:   maxRetries,
		SessionID:    c.id,
		// A sub-agent runs its own loop: the main loop's middleware does not
		// reach it.
		Middleware: c.middleware(),
		Emit:       agentEmit(c.agents),
	}
	var agents []coresub.Agent
	for _, def := range subagent.Definitions(c.cwd, model.small) {
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

// withToolSearch defers the non-core tools behind tool_search when the model
// supports it.
func withToolSearch(all []agentcore.Tool, provider, model string) []agentcore.Tool {
	if !supportsToolSearch(provider, model) {
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

// supportsToolSearch reports whether a model takes deferred tools. Only
// Claude 4.5 and later (not Haiku) do in litellm today: the OpenAI providers
// honor neither defer_loading nor tool_reference blocks, so activating a tool
// there would re-add its schema mid-session and invalidate the prompt cache.
func supportsToolSearch(provider, model string) bool {
	p, m := strings.ToLower(provider), strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if p != "anthropic" && !strings.HasPrefix(m, "claude") {
		return false
	}
	if strings.Contains(m, "haiku") {
		return false
	}
	m = strings.TrimPrefix(m, "claude-")
	for _, family := range []string{"sonnet-", "opus-"} {
		m = strings.TrimPrefix(m, family)
	}
	return versionAtLeast(m, 4, 5)
}

func versionAtLeast(s string, major, minor int) bool {
	var ma, mi int
	if n, _ := fmt.Sscanf(s, "%d.%d", &ma, &mi); n == 2 {
		return ma > major || (ma == major && mi >= minor)
	}
	if n, _ := fmt.Sscanf(s, "%d-%d", &ma, &mi); n == 2 {
		return ma > major || (ma == major && mi >= minor)
	}
	if n, _ := fmt.Sscanf(s, "%d", &ma); n == 1 {
		return ma > major
	}
	return false
}
