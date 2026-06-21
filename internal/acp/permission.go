package acp

import (
	"context"

	acp "github.com/coder/acp-go-sdk"

	"github.com/voocel/agentcore/permission"
)

// approve is installed as the approval.Engine Approver: it forwards each
// permission decision to the editor via session/request_permission. For
// dangerous paths (p.OutsideRoots) it offers only one-time allow / reject —
// never a persistent allow — matching the engine's force-ask policy.
func (a *acpAgent) approve(ctx context.Context, p permission.Prompt) (permission.Choice, error) {
	opts := []acp.PermissionOption{
		{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow_once"},
	}
	if !p.OutsideRoots {
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
	resp, err := a.conn.Load().RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: a.sid,
		Options:   opts,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(p.Tool),
			Title:      acp.Ptr(title),
			Kind:       acp.Ptr(toolKind(p.Tool)),
		},
	})
	if err != nil {
		return permission.ChoiceDeny, err
	}
	if resp.Outcome.Selected == nil { // cancelled or no selection
		return permission.ChoiceDeny, nil
	}
	switch resp.Outcome.Selected.OptionId {
	case "allow_once":
		return permission.ChoiceAllowOnce, nil
	case "allow_always":
		return permission.ChoiceAllowAlways, nil
	default:
		return permission.ChoiceDeny, nil
	}
}
