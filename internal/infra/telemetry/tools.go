package telemetry

import (
	"context"

	"github.com/voocel/agentcore"
	"go.opentelemetry.io/otel/attribute"
)

// ToolMiddleware observes each tool call with its arguments and the result
// the model gets. Put outermost, it holds the call's approval too, and a
// refused call shows as one. A call of agentcore's subagent tool is left to
// the runs it starts (see SubAgent): a node for it would show each
// sub-agent twice.
func (t *Tracer) ToolMiddleware() agentcore.ToolMiddleware {
	if t == nil {
		return nil
	}
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolFunc) (agentcore.Result, error) {
		if call.Name == "subagent" {
			return next(ctx, call)
		}
		ctx, o := t.start(ctx, "execute_tool "+call.Name, typeTool, call.Name,
			attribute.String(attrOperation, "execute_tool"),
			attribute.String(attrToolName, call.Name),
			attribute.String(attrToolCallID, call.ID),
			attribute.String(attrToolArgs, string(call.Args)),
		)
		res, err := next(ctx, call)
		result := res.Text()
		if err != nil {
			result = err.Error() // what agentcore hands the model instead
		}
		o.span.SetAttributes(attribute.String(attrToolResult, result))
		if err == nil && res.IsError {
			o.span.SetAttributes(attribute.String(attrLevel, "ERROR"), attribute.String(attrStatusMessage, res.Text()))
		}
		o.End("", err)
		return res, err
	}
}

// Approval observes asking the user to approve what asked describes, as a
// guardrail of the call that asked; its output is the user's choice.
func (t *Tracer) Approval(ctx context.Context, asked string) (context.Context, *Observation) {
	if t == nil {
		return ctx, nil
	}
	return t.start(ctx, "approval", typeGuardrail, "approval", attribute.String(attrInput, asked))
}
