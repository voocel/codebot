package acp

import (
	"context"

	acp "github.com/coder/acp-go-sdk"

	"github.com/voocel/codebot/internal/interact"
)

var _ interact.UI = (*Server)(nil)

// Approve offers a persistent allow only when there is a rule to remember.
func (s *Server) Approve(ctx context.Context, p interact.Approval) (interact.Verdict, error) {
	opts := []acp.PermissionOption{
		{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow_once"},
	}
	if p.Remember != "" {
		opts = append(opts, acp.PermissionOption{
			Kind: acp.PermissionOptionKindAllowAlways, Name: "Always allow " + p.Remember, OptionId: "allow_always",
		})
	}
	opts = append(opts, acp.PermissionOption{
		Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject",
	})

	title := p.Summary
	if title == "" {
		title = p.Tool
	}
	// A hook command has no tool call of its own for the editor to show, so
	// the approval is labeled with the tool name.
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
		return interact.Verdict{Choice: interact.Deny}, err
	}
	choice := interact.Deny // also when cancelled, with nothing selected
	if resp.Outcome.Selected != nil {
		switch resp.Outcome.Selected.OptionId {
		case "allow_once":
			choice = interact.AllowOnce
		case "allow_always":
			choice = interact.AllowAlways
		}
	}
	return interact.Verdict{Choice: choice}, nil
}

// ACP can't pose questions, so the App runs without ask_user.
func (s *Server) Ask(context.Context, []interact.Question) (interact.Answers, error) {
	return interact.Answers{}, interact.ErrUnsupported
}
