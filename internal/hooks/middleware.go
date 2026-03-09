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
		if err := r.RunPreToolUse(ctx, call.Name, call.Args); err != nil {
			return nil, err
		}

		output, execErr := next(ctx, call.Args)

		r.RunPostToolUse(ctx, call.Name, call.Args, output, execErr != nil)

		return output, execErr
	}
}
