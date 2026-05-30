package hooks

import (
	"context"
	"encoding/json"

	"github.com/voocel/agentcore"
)

// Middleware returns a ToolMiddleware that runs PreToolUse and PostToolUse hooks
// around each tool execution.
func (r *Runner) Middleware() agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolExecuteFunc) (json.RawMessage, error) {
		dec, err := r.RunPreToolUse(ctx, call.Name, call.Args)
		if err != nil {
			return nil, err
		}

		args := call.Args
		if len(dec.UpdatedInput) > 0 {
			args = dec.UpdatedInput
		}

		output, execErr := next(ctx, args)

		r.RunPostToolUse(ctx, call.Name, args, output, execErr != nil)

		return output, execErr
	}
}
