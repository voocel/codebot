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

// retryPolicy paces the model calls the agent and its sub-agents make again
// after a transient failure.
var retryPolicy = retry.Policy{MaxAttempts: 6, InitialDelay: time.Second, MaxDelay: 30 * time.Second}

// configure hands the session the spec for the conversation's current state;
// it applies from the next run.
func (c *Conversation) configure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configureLocked()
}

// configureLocked is configure for callers holding c.mu. Building and handing
// over the spec under one lock keeps concurrent changes in order.
func (c *Conversation) configureLocked() { c.session.Configure(c.specLocked()) }

// middleware wraps every tool call, the main agent's and its sub-agents',
// outermost first: PreToolUse hooks, the permission check, telemetry,
// PostToolUse hooks, tracking changes for PostStopValidation, and output
// limiting. The limiter is innermost, so hooks and telemetry see the result
// the model will see.
func (c *Conversation) middleware() []agentcore.ToolMiddleware {
	a := c.app
	out := []agentcore.ToolMiddleware{c.hooks.PreToolUse(), a.permissions.Middleware(c.skillGrants, a.toolPermission)}
	if mw := a.tracer.ToolMiddleware(); mw != nil {
		out = append(out, mw)
	}
	return append(out, c.hooks.PostToolUse(), c.validation.Track, c.limiter.Middleware())
}

// specLocked builds the RunSpec for the conversation's current state. It is
// the only place a run's configuration comes from. Callers hold c.mu.
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
		// Breakpoints on the freshest message and where the call before
		// ended, so each call reads the one before from the cache.
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
			// The date is the run's.
			return contextMessages(append([]prompt.Part{prompt.Environment(cwd, time.Now())}, parts...), history)
		},
	}
}

// wrapRun gives each run the conversation's working directory, a trace span
// and a file checkpoint for Undo.
func (c *Conversation) wrapRun(ctx context.Context) (context.Context, func(error)) {
	// Read live: a worktree entered mid-run moves the run's later tool calls.
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

// stop is the run's Stop: a failing PostStopValidation sends the agent back
// to fix it.
func (c *Conversation) stop(ctx context.Context, _ agentcore.StopInfo) ([]agentcore.Message, error) {
	if fix := c.validation.Check(ctx); fix != "" {
		return []agentcore.Message{reminderMessage(fix)}, nil
	}
	return nil, nil
}
