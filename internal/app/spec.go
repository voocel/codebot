package app

import (
	"context"
	"slices"
	"sync/atomic"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/compact"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm/retry"

	"github.com/voocel/codebot/internal/hooks"
	"github.com/voocel/codebot/internal/prompt"
	"github.com/voocel/codebot/internal/provider"
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
	var out []agentcore.ToolMiddleware
	if c.hooks != nil {
		out = append(out, c.hooks.PreToolUse())
	}
	out = append(out, a.approval.Middleware(c.skillGrants, a.toolPermission))
	if mw := a.tracer.ToolMiddleware(); mw != nil {
		out = append(out, mw)
	}
	if c.hooks != nil {
		out = append(out, c.hooks.PostToolUse(), c.validator.track)
	}
	return append(out, c.limiter.Middleware())
}

// specLocked builds the RunSpec for the conversation's current state. It is
// the only place a run's configuration comes from. Callers hold c.mu.
func (c *Conversation) specLocked() session.RunSpec {
	a := c.app
	_, mcpInstructions := a.mcpSnapshot()
	tools := withToolSearch(slices.Concat(c.tools, c.mcpTools), c.model.model.Client)
	cwd := c.cwd
	parts := append(slices.Clone(c.workspace), prompt.MCP(mcpInstructions), prompt.DeferredTools(deferredNames(tools)))

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
		Compactor:          compact.Summarizer{},
		CompactAt:          c.model.compactAt,
		// Breakpoints on the freshest message and where the call before
		// ended, so each call reads the one before from the cache.
		Cache: a.cache(),
	}
	if c.hooks != nil {
		cfg.OnStop = c.validator.stop
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

// kindReminder marks a message the harness adds for the model, such as a
// validation failure to fix or context a hook adds.
const kindReminder = "reminder"

// validation runs the PostStopValidation hooks when a run would stop after
// changing the repository, and sends the agent back once to fix a failure.
type validation struct {
	hooks  *hooks.Runner
	dirty  atomic.Bool // the repository changed since the last passing validation
	failed bool        // the last stop was refused; touched only by the run
}

// track marks the repository changed after a successful mutating call.
func (v *validation) track(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolFunc) (agentcore.Result, error) {
	res, err := next(ctx, call)
	if err == nil && !res.IsError {
		switch call.Name {
		case "bash", "write", "edit":
			v.dirty.Store(true)
		}
	}
	return res, err
}

// stop is the run's Stop: with the repository changed, the run goes on to
// fix a failing validation, once per stop.
func (v *validation) stop(ctx context.Context, _ agentcore.StopInfo) ([]agentcore.Message, error) {
	if !v.dirty.Load() {
		return nil, nil
	}
	out := v.hooks.RunPostStopValidation(ctx)
	if out == "" {
		v.dirty.Store(false)
		v.failed = false
		return nil, nil
	}
	if v.failed {
		// One fix attempt per stop; the next run validates again.
		v.failed = false
		return nil, nil
	}
	v.failed = true
	return []agentcore.Message{reminderMessage("The PostStopValidation hook failed. Fix the problem based on the following output:\n" + out)}, nil
}
