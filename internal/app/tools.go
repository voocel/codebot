package app

import (
	"cmp"
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
	coresub "github.com/voocel/agentcore/subagent"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/permission"
	"github.com/voocel/codebot/internal/agent/subagent"
	"github.com/voocel/codebot/internal/agent/tools"
)

// buildTools leaves out MCP tools, which specLocked adds. Callers hold c.mu.
func (c *Conversation) buildTools() []agentcore.Tool {
	a := c.app
	ws := agentcoretools.Workspace{Dir: c.cwd, FS: c.frontend.FS, Files: c.files, Tasks: c.tasks}
	if a.sandbox != nil {
		ws.Sandbox = c.sandboxed
	}

	// Sub-agents get every tool up to here, re-pooled per agent.
	out := append(ws.Tools(),
		tools.NewWebFetch(a.settings.SearchProvider, a.settings.SearchAPIKey),
		tools.NewWebSearch(a.settings.SearchProvider, a.settings.SearchAPIKey),
		tools.NewTodoWrite(),
	)
	if c.frontend.Interactive {
		out = append(out, tools.NewAskUser(c.frontend.UI))
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

// sandboxed wraps a bash command in the sandbox. It may write where the call
// runs, in the checkout the conversation opened in and in the write roots,
// but not to the persistent paths there or to local plugins, whose code
// codebot runs.
func (c *Conversation) sandboxed(ctx context.Context, argv []string) []string {
	roots := c.app.permissions.Roots()
	writable := append([]string{cmp.Or(agentcoretools.CwdFromContext(ctx), c.home), c.home}, roots.WriteRoots...)
	return c.app.sandbox.Wrap(argv, writable, append(persistentIn(writable), roots.Protected...))
}

// persistentIn returns the persistent paths in dirs, the directory git runs
// hooks from included.
func persistentIn(dirs []string) []string {
	var out []string
	for _, dir := range dirs {
		for _, name := range permission.Persistent {
			out = append(out, filepath.Join(dir, name))
		}
		if hooks := gitHooks(dir); hooks != "" {
			out = append(out, hooks)
		}
	}
	return out
}

// gitHooks returns the directory git runs hooks from in the repository at
// dir, such as Husky's that core.hooksPath names, or "" outside one.
func gitHooks(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-path", "hooks").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (c *Conversation) buildSubagents(pool []agentcore.Tool, ws agentcoretools.Workspace) agentcore.Tool {
	a, model := c.app, c.model
	deps := subagent.BuildDeps{
		Workspace:    ws,
		MainTools:    pool,
		DefaultModel: subagent.Model{Model: model.model, CompactAt: model.compactAt},
		ResolveModel: func(name string) (subagent.Model, error) { return a.subagentModel(model.provider, name) },
		Retry:        retryPolicy,
		SessionID:    c.id,
		// A sub-agent runs its own loop, so it needs its own middleware.
		Middleware: c.middleware(),
		Emit:       agentEmit(c.agents),
		WrapRun: func(ctx context.Context, s coresub.Spawn, prompt string) (context.Context, func(string, error)) {
			return a.tracer.SubAgent(ctx, s.Agent, s.ID, prompt)
		},
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

func (c *Conversation) forkSkill(ctx context.Context, args json.RawMessage) (agentcore.Result, error) {
	c.mu.Lock()
	agents := c.subagents
	c.mu.Unlock()
	return agents.Run(ctx, args)
}

// coreToolNames stay visible under tool search so the first turn needs no
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

// withToolSearch defers non-core tools only when the adapter supports
// deferred tools natively. Otherwise each tool found would be added to the
// request mid-session, which invalidates the prompt cache and Claude's
// thinking.
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
