package app

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/compact"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm/retry"

	"github.com/voocel/codebot/internal/agent/permission"
	"github.com/voocel/codebot/internal/agent/prompt"
	"github.com/voocel/codebot/internal/infra/provider"
	"github.com/voocel/codebot/internal/infra/telemetry"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/session"
)

var retryPolicy = retry.Policy{MaxAttempts: 6, InitialDelay: time.Second, MaxDelay: 30 * time.Second}

// configureLocked requires c.mu: building and handing over the spec under
// one lock keeps concurrent changes in order. It applies from the next run.
func (c *Conversation) configureLocked() { c.session.Configure(c.specLocked()) }

// middleware applies to the main agent and its sub-agents, outermost first.
// Tracing is outermost so a call's trace holds its approval, and a refused
// call is traced too; the limiter is innermost so hooks and tracing see the
// result the model will see.
func (c *Conversation) middleware() []agentcore.ToolMiddleware {
	a := c.app
	scope := permission.Scope{Mode: c.Mode, UI: c.frontend.UI, Grants: c.skillGrants, Meta: a.toolPermission}
	if a.tracer != nil && scope.UI != nil {
		scope.UI = tracedApprovals{scope.UI, a.tracer}
	}
	if !a.within(c.home) {
		scope.Roots = []string{c.home}
	}
	var out []agentcore.ToolMiddleware
	if mw := a.tracer.ToolMiddleware(); mw != nil {
		out = append(out, mw)
	}
	return append(out, c.hooks.PreToolUse(), a.permissions.Middleware(scope), c.hooks.PostToolUse(), c.validation.Track, c.limiter.Middleware())
}

// tracedApprovals traces the approvals the UI gives.
type tracedApprovals struct {
	interact.UI
	tracer *telemetry.Tracer
}

func (u tracedApprovals) Approve(ctx context.Context, req interact.Approval) (interact.Verdict, error) {
	ctx, o := u.tracer.Approval(ctx, cmp.Or(req.Summary, req.Tool))
	v, err := u.UI.Approve(ctx, req)
	o.End(string(v.Choice), err)
	return v, err
}

// specLocked is the only source of a run's configuration. Callers hold c.mu.
func (c *Conversation) specLocked() session.RunSpec {
	a := c.app
	tools := withToolSearch(slices.Concat(c.tools, c.mcpTools), c.model.model.Client)
	cwd := c.cwd
	parts := append(slices.Clone(c.workspace), prompt.MCP(a.offered.Load().instructions), prompt.DeferredTools(deferredNames(tools)))

	model := c.model.model
	model.Request.Thinking = provider.Thinking(c.model.effort)
	ran := map[string]string{"model": c.model.provider + "/" + c.model.name, "effort": c.model.effort}
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
		WrapRun: func(ctx context.Context, inputs []agentcore.Message) (context.Context, func([]agentcore.Message, error)) {
			return c.wrapRun(ctx, inputs, ran)
		},
		Checkpoint: c.checkpoint,
		Context: func(history []agentcore.Message) []agentcore.Message {
			// Use the date the run starts.
			return contextMessages(append([]prompt.Part{prompt.Environment(cwd, time.Now())}, parts...), history)
		},
	}
}

// runContext is ctx for the conversation's work: tools read its cwd live,
// so a worktree entered mid-run applies to later calls, and its traces are
// the conversation's.
func (c *Conversation) runContext(ctx context.Context) context.Context {
	return telemetry.WithConversation(agentcoretools.WithCwd(ctx, c.Cwd), c.id, c.frontend.Name)
}

// wrapRun traces the run as a turn, ran being the model it runs.
func (c *Conversation) wrapRun(ctx context.Context, inputs []agentcore.Message, ran map[string]string) (context.Context, func([]agentcore.Message, error)) {
	meta := maps.Clone(ran)
	meta["mode"], meta["cwd"] = string(c.Mode()), c.Cwd()
	ctx, turn := c.app.tracer.Turn(c.runContext(ctx), inputText(inputs), meta)
	return ctx, func(history []agentcore.Message, err error) {
		turn.End(agentcore.LastResponse(history).Text(), err)
		c.mu.Lock()
		c.grants = nil
		c.mu.Unlock()
	}
}

// inputText is what a run's inputs say, as the user put it.
func inputText(inputs []agentcore.Message) string {
	texts := make([]string, 0, len(inputs))
	for _, m := range inputs {
		text, ok := UserText(m)
		if !ok {
			text = m.Text()
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, "\n\n")
}

// checkpoint is best effort: a failed checkpoint must not block the run, and
// leaves it one only the conversation can go back to.
func (c *Conversation) checkpoint() (dir, tree string) {
	if c.snapshots == nil {
		return "", ""
	}
	dir, tree, err := c.snapshots.Checkpoint()
	if err != nil {
		return "", ""
	}
	return dir, tree
}

// stop sends the agent back to fix a failing PostStopValidation.
func (c *Conversation) stop(ctx context.Context, _ agentcore.StopInfo) ([]agentcore.Message, error) {
	if fix := c.validation.Check(ctx); fix != "" {
		return []agentcore.Message{reminderMessage(fix)}, nil
	}
	return nil, nil
}
