package hooks

import (
	"context"
	"encoding/json"

	"github.com/voocel/agentcore"
)

// PreToolUse runs before the permission check, so rewritten arguments are
// what gets checked and run.
func (r *Runner) PreToolUse() agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolFunc) (agentcore.Result, error) {
		dec, err := r.preToolUse(ctx, call.Name, call.Args)
		if err != nil {
			return agentcore.ErrorResult(err.Error()), nil
		}
		if len(dec.UpdatedInput) > 0 {
			call.Args = dec.UpdatedInput
		}
		return next(ctx, call)
	}
}

// PostToolUse passes the result text to the hooks as a JSON string.
func (r *Runner) PostToolUse() agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolFunc) (agentcore.Result, error) {
		res, err := next(ctx, call)
		text := res.Text()
		if err != nil {
			text = err.Error()
		}
		output, _ := json.Marshal(text) // a string always encodes
		r.postToolUse(call.Name, call.Args, output, err != nil || res.IsError)
		return res, err
	}
}
