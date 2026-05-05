package approval

import (
	"context"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/permission"
)

// AsToolGate adapts the approval Engine to agentcore.ToolGate.
// The kernel is permission-agnostic; the harness owns the policy.
//
// The adapter extracts permission.Metadata from the tool when the tool
// implements the optional PermissionMetadata accessor — this keeps the
// kernel independent of the permission package while letting harness
// tools advertise their capability/key/summary hints.
func (e *Engine) AsToolGate() agentcore.ToolGate {
	return func(ctx context.Context, req agentcore.GateRequest) (*agentcore.GateDecision, error) {
		permReq := permission.Request{
			ToolID:    req.Call.ID,
			ToolName:  req.Call.Name,
			ToolLabel: req.ToolLabel,
			Args:      req.Call.Args,
			Preview:   req.Preview,
			Metadata:  extractMetadata(req.Tool),
		}
		decision, err := e.Decide(ctx, permReq)
		if err != nil {
			return nil, err
		}
		if decision == nil {
			return nil, nil
		}
		gd := &agentcore.GateDecision{Allowed: decision.Allowed()}
		if !gd.Allowed {
			gd.Reason = decision.Reason
		}
		return gd, nil
	}
}

func extractMetadata(tool agentcore.Tool) permission.Metadata {
	if tool == nil {
		return permission.Metadata{}
	}
	if mp, ok := tool.(interface {
		PermissionMetadata() permission.Metadata
	}); ok {
		return mp.PermissionMetadata()
	}
	return permission.Metadata{}
}
