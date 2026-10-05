package hooks

import (
	"context"
	"encoding/json"

	"github.com/voocel/agentcore"
)

// PreToolUse returns a ToolMiddleware that runs the PreToolUse hooks before
// the rest of the chain, the permission check among it. A blocking hook
// refuses the call; arguments a hook rewrites are what the rest decides on
// and what the tool runs with.
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

// PostToolUse returns a ToolMiddleware that fires the PostToolUse hooks after
// each call, with the arguments it ran with and its result's text as a JSON
// string.
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
