package hooks

import (
	"context"
	"encoding/json"

	"github.com/voocel/agentcore"
)

// WrapGate returns a ToolGate that runs PreToolUse hooks before delegating to
// next (the permission gate). Ordering matches Claude Code: hooks fire first —
// a blocking hook denies the call, an updated_input rewrite is applied to the
// request — and the permission decision is then made on the FINAL arguments.
// The rewrite is surfaced to the kernel via GateDecision.UpdatedArgs so the
// tool executes exactly what was approved.
func (r *Runner) WrapGate(next agentcore.ToolGate) agentcore.ToolGate {
	return func(ctx context.Context, req agentcore.GateRequest) (*agentcore.GateDecision, error) {
		dec, err := r.RunPreToolUse(ctx, req.Call.Name, req.Call.Args)
		if err != nil {
			return &agentcore.GateDecision{Allowed: false, Reason: err.Error()}, nil
		}
		if len(dec.UpdatedInput) > 0 {
			req.Call.Args = dec.UpdatedInput
		}
		decision, err := next(ctx, req)
		if err != nil {
			return decision, err
		}
		// nil decision means "no opinion" (allow) — the hook rewrite must
		// still reach the kernel, so synthesize an allow that carries it.
		if decision == nil {
			if len(dec.UpdatedInput) > 0 {
				return &agentcore.GateDecision{Allowed: true, UpdatedArgs: dec.UpdatedInput}, nil
			}
			return nil, nil
		}
		if !decision.Allowed {
			return decision, nil
		}
		// The permission gate's own rewrite (e.g. ask_user answer backfill) is
		// computed from the hook-updated args, so it already subsumes them.
		if len(decision.UpdatedArgs) == 0 && len(dec.UpdatedInput) > 0 {
			d := *decision
			d.UpdatedArgs = dec.UpdatedInput
			return &d, nil
		}
		return decision, nil
	}
}

// Middleware returns a ToolMiddleware that fires PostToolUse hooks after each
// tool execution. PreToolUse runs earlier, inside the tool gate (WrapGate),
// so permission decisions see hook-rewritten arguments; by the time this
// middleware runs, call.Args already carries the approved final form.
func (r *Runner) Middleware() agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolExecuteFunc) (json.RawMessage, error) {
		output, execErr := next(ctx, call.Args)
		r.RunPostToolUse(ctx, call.Name, call.Args, output, execErr != nil)
		return output, execErr
	}
}
