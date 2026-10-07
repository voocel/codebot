package app

import (
	"context"
	"slices"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/compact"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm/retry"

	"github.com/voocel/codebot/internal/agent/prompt"
	"github.com/voocel/codebot/internal/infra/provider"
	"github.com/voocel/codebot/internal/session"
)

var retryPolicy = retry.Policy{MaxAttempts: 6, InitialDelay: time.Second, MaxDelay: 30 * time.Second}

// configureLocked requires c.mu: building and handing over the spec under
// one lock keeps concurrent changes in order. It applies from the next run.
func (c *Conversation) configureLocked() { c.session.Configure(c.specLocked()) }

// middleware applies to the main agent and its sub-agents, outermost first.
// The limiter is innermost so hooks and telemetry see the result the model
// will see.
func (c *Conversation) middleware() []agentcore.ToolMiddleware {
	a := c.app
	out := []agentcore.ToolMiddleware{c.hooks.PreToolUse(), a.permissions.Middleware(c.skillGrants, a.toolPermission)}
	if mw := a.tracer.ToolMiddleware(); mw != nil {
		out = append(out, mw)
	}
	return append(out, c.hooks.PostToolUse(), c.validation.Track, c.limiter.Middleware())
}

// specLocked is the only source of a run's configuration. Callers hold c.mu.
func (c *Conversation) specLocked() session.RunSpec {
	a := c.app
	tools := withToolSearch(slices.Concat(c.tools, c.mcpTools), c.model.model.Client)
	cwd := c.cwd
	parts := append(slices.Clone(c.workspace), prompt.MCP(a.offered.Load().instructions), prompt.DeferredTools(deferredNames(tools)))

	model := c.model.model
	model.Request.Thinking = provider.Thinking(c.model.effort)
	cfg := agentcore.Config{
		// One conversation, one prompt-cache key.
		Model:              provider.WithCacheKey(model, c.id),
		System:             c.system,
		Tools:              tools,
		Middleware:         c.middleware(),
		MaxTurns:           a.settings.MaxTurns,
		Retry:              retryPolicy,
		MaxToolErrors:      3,
		MaxToolConcurrency: 4,
		Compactor:          compact.Summarizer{Notes: agentcoretools.FileOps},
		CompactAt:          c.model.compactAt,
		// Breakpoints go on the newest message and where the previous call
		// ended, so each call reads the previous one from the cache.
		Cache:  a.cache(),
		OnStop: c.stop,
	}
	return session.RunSpec{
		Provider: c.model.provider,
		Model:    c.model.name,
		Effort:   c.model.effort,
		Window:   c.model.window,
		Config:   cfg,
		WrapRun:  c.wrapRun,
		Context: func(history []agentcore.Message) []agentcore.Message {
			// Use the date the run starts.
			return contextMessages(append([]prompt.Part{prompt.Environment(cwd, time.Now())}, parts...), history)
		},
	}
}

func (c *Conversation) wrapRun(ctx context.Context) (context.Context, func(error)) {
	// Read the cwd live so a worktree entered mid-run applies to later tool
	// calls.
	ctx = agentcoretools.WithCwd(ctx, c.Cwd)
	ctx, span := c.app.tracer.StartRun(ctx, "agent run")
	if c.snapshots != nil {
		_, _ = c.snapshots.Track() // best effort: a failed checkpoint must not block the run
	}
	return ctx, func(err error) {
		span.End(err)
		c.mu.Lock()
		c.grants = nil
		c.mu.Unlock()
	}
}

// stop sends the agent back to fix a failing PostStopValidation.
func (c *Conversation) stop(ctx context.Context, _ agentcore.StopInfo) ([]agentcore.Message, error) {
	if fix := c.validation.Check(ctx); fix != "" {
		return []agentcore.Message{reminderMessage(fix)}, nil
	}
	return nil, nil
}
