package acp

import (
	"context"

	acp "github.com/coder/acp-go-sdk"

	"github.com/voocel/codebot/internal/interact"
)

var _ interact.UI = (*Server)(nil)

// Approve forwards a permission decision to the editor via
// session/request_permission. A OnceOnly approval offers only one-time allow
// and reject, never a persistent allow.
func (s *Server) Approve(ctx context.Context, p interact.Approval) (interact.Choice, error) {
	opts := []acp.PermissionOption{
		{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow_once"},
	}
	if !p.OnceOnly {
		opts = append(opts, acp.PermissionOption{
			Kind: acp.PermissionOptionKindAllowAlways, Name: "Always allow", OptionId: "allow_always",
		})
	}
	opts = append(opts, acp.PermissionOption{
		Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject",
	})

	title := p.Summary
	if title == "" {
		title = p.Tool
	}
	// The editor already shows the tool call the approval is for; a hook
	// command has none, so it is named by the tool.
	id := p.ToolID
	if id == "" {
		id = p.Tool
	}
	resp, err := s.conn.Load().RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: s.sessionID(),
		Options:   opts,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(id),
			Title:      acp.Ptr(title),
			Kind:       acp.Ptr(toolKind(p.Tool)),
		},
	})
	if err != nil {
		return interact.Deny, err
	}
	if resp.Outcome.Selected == nil { // cancelled or no selection
		return interact.Deny, nil
	}
	switch resp.Outcome.Selected.OptionId {
	case "allow_once":
		return interact.AllowOnce, nil
	case "allow_always":
		return interact.AllowAlways, nil
	default:
		return interact.Deny, nil
	}
}

// Ask is unsupported: ACP has no way to pose questions, so the App runs
// without ask_user.
func (s *Server) Ask(context.Context, []interact.Question) (interact.Answers, error) {
	return interact.Answers{}, interact.ErrUnsupported
}
