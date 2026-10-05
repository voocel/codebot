package hooks

import (
	"context"
	"sync/atomic"

	"github.com/voocel/agentcore"
)

// Validation runs the PostStopValidation hooks when a run would stop after
// changing the repository, and sends the agent back once to fix a failure. A
// conversation has one over its Runner.
type Validation struct {
	runner *Runner
	dirty  atomic.Bool // the repository changed since the last passing validation
	failed bool        // the last stop was refused; touched only by the run
}

// NewValidation returns a conversation's validation over r.
func NewValidation(r *Runner) *Validation { return &Validation{runner: r} }

// Track is a ToolMiddleware marking the repository changed after a
// successful mutating call.
func (v *Validation) Track(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolFunc) (agentcore.Result, error) {
	res, err := next(ctx, call)
	if err == nil && !res.IsError {
		switch call.Name {
		case "bash", "write", "edit":
			v.dirty.Store(true)
		}
	}
	return res, err
}

// Check is called as a run would stop. With the repository changed, it runs
// the hooks and returns what the agent is to fix, once per stop; "" lets the
// run stop.
func (v *Validation) Check(ctx context.Context) string {
	if !v.dirty.Load() {
		return ""
	}
	out := v.runner.runPostStopValidation(ctx)
	if out == "" {
		v.dirty.Store(false)
		v.failed = false
		return ""
	}
	if v.failed {
		// One fix attempt per stop; the next run validates again.
		v.failed = false
		return ""
	}
	v.failed = true
	return "The PostStopValidation hook failed. Fix the problem based on the following output:\n" + out
}
