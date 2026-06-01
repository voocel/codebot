package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
)

// SendMessageTool is a unified parent/peer message delivery tool. Replaces
// the old send_to_subagent: a single tool now routes to either a teammate
// mailbox or a subagent's pending-message queue, picked by what `to` resolves
// to in the team registry / task runtime.
//
// Resolution order:
//  1. team.Registry name lookup (teammate or the reserved team-lead) — uses mailbox
//  2. task.Runtime task-ID lookup (subagent) — uses AppendPending
//
// The sender's identity is taken from team.IdentityFromContext(ctx). The
// leader (no identity) sends as "team-lead"; a teammate sends as its own name.
type SendMessageTool struct {
	rt    *task.Runtime
	reg   *team.Registry
	waker TeammateWaker
}

// TeammateWaker re-spawns a dormant teammate from its persisted transcript and
// delivers a message as its opening turn. send_message consults it when a
// target name has no live teammate behind it — the lazy, message-driven resume
// path. Implemented by internal/agent.TeammateWaker; a nil waker disables wake,
// so a stopped teammate simply reports as not found.
type TeammateWaker interface {
	Wake(ctx context.Context, name, prompt string) (bool, error)
}

// NewSendMessageTool wires the tool to the shared task runtime and team
// registry. Both are required.
func NewSendMessageTool(rt *task.Runtime, reg *team.Registry) *SendMessageTool {
	return &SendMessageTool{rt: rt, reg: reg}
}

// SetWaker enables lazy teammate resume: when send_message targets a teammate
// name with no live mailbox, the waker re-spawns it from its saved transcript
// with the message as its opening turn. Optional; nil keeps stop-is-final.
func (t *SendMessageTool) SetWaker(w TeammateWaker) { t.waker = w }

func (t *SendMessageTool) Name() string  { return "send_message" }
func (t *SendMessageTool) Label() string { return "Send Message" }

func (t *SendMessageTool) Description() string {
	return `Send a message to another agent — either a teammate in your active team or a background subagent.

For a teammate, set ` + "`to`" + ` to their name (e.g. "researcher") or to "team-lead" to message the leader. The message lands in their mailbox and is picked up at their next turn boundary.

For a subagent, set ` + "`to`" + ` to the task ID returned by the subagent tool (e.g. "bg-3"). The message is delivered as a user turn at the subagent's next tool-round boundary.

Use this when you need to:
- assign work to a teammate or peer
- relay user feedback or new context mid-flight
- correct or extend a running agent's instructions
- respond to a teammate's question

The target must still be running. If it has completed/failed/killed, the call returns an error.`
}

func (t *SendMessageTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("to", schema.String(
			"Recipient: a teammate's name, the literal 'team-lead', or a subagent task ID (e.g. 'bg-3').",
		)).Required(),
		schema.Property("message", schema.String(
			"Plain text content delivered to the target.",
		)).Required(),
	)
}

func (t *SendMessageTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		To      string `json:"to"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	a.To = strings.TrimSpace(a.To)
	if a.To == "" {
		return json.Marshal("Validation error: to is required")
	}
	if strings.TrimSpace(a.Message) == "" {
		return json.Marshal("Validation error: message is required")
	}

	// 1. Team registry lookup (teammate / team-lead)
	if t.reg != nil && t.reg.HasTeam() {
		if _, ok := t.reg.TaskID(a.To); ok {
			return t.sendToTeam(ctx, a.To, a.Message)
		}
		// Not live. A teammate that exited (or a prior session's teammate that
		// hasn't been re-spawned yet) is unregistered but may still have a
		// persisted roster entry + transcript. Wake it lazily, delivering this
		// message as its opening turn — the message IS the resume prompt.
		if t.waker != nil {
			woke, err := t.waker.Wake(ctx, a.To, a.Message)
			if err != nil {
				return json.Marshal(fmt.Sprintf("Could not resume teammate %q: %v", a.To, err))
			}
			if woke {
				return json.Marshal(map[string]any{
					"success": true,
					"message": fmt.Sprintf("Resumed teammate %s from its saved transcript; your message is its next turn.", a.To),
				})
			}
			// Wake didn't spawn: either the name is unknown, or a concurrent
			// wake already revived it. If it is live now, deliver to its mailbox.
			if _, ok := t.reg.TaskID(a.To); ok {
				return t.sendToTeam(ctx, a.To, a.Message)
			}
		}
	}

	// 2. Task runtime lookup (subagent by task ID)
	entry := t.rt.Get(a.To)
	if entry == nil {
		return json.Marshal(fmt.Sprintf("No agent or task matches %q. For teammates use their name; for subagents use the task ID from /tasks.", a.To))
	}
	if entry.Type != task.TypeSubAgent {
		return json.Marshal(fmt.Sprintf("Task %s is type %q; send_message can only deliver to subagents and teammates.", a.To, entry.Type))
	}
	switch t.rt.AppendPending(a.To, a.Message) {
	case task.AppendOK:
		return json.Marshal(map[string]any{
			"success": true,
			"message": fmt.Sprintf("Message queued for subagent %s (%s); delivered at its next tool-round boundary.", a.To, entry.Agent),
		})
	case task.AppendNotFound:
		return json.Marshal(fmt.Sprintf("Task %s no longer exists.", a.To))
	case task.AppendTerminal:
		return json.Marshal(map[string]any{
			"success": false,
			"message": fmt.Sprintf("Subagent %s has already finished (status=%s); start a new one instead.", a.To, entry.Status),
		})
	}
	return json.Marshal("internal error: unexpected AppendStatus")
}

// sendToTeam writes to a registered teammate's mailbox. Sender identity comes
// from ctx (set by the teammate runner via team.WithIdentity); the leader
// sends as TeamLeadName when no identity is in ctx.
func (t *SendMessageTool) sendToTeam(ctx context.Context, to, content string) (json.RawMessage, error) {
	mb := t.reg.Mailbox(to)
	if mb == nil {
		return json.Marshal(fmt.Sprintf("Recipient %q is registered but has no mailbox (internal state inconsistent).", to))
	}

	sender := team.TeamLeadName
	var senderColor string
	if id := team.IdentityFromContext(ctx); id != nil {
		sender = id.AgentName
		senderColor = id.Color
	}
	if sender == to {
		return json.Marshal("Validation error: cannot send a message to yourself")
	}

	err := mb.Send(team.Message{From: sender, Text: content, Color: senderColor})
	if err != nil {
		if errors.Is(err, team.ErrClosed) {
			return json.Marshal(fmt.Sprintf("Recipient %q has shut down — mailbox is closed.", to))
		}
		return nil, fmt.Errorf("send to %s: %w", to, err)
	}
	return json.Marshal(map[string]any{
		"success": true,
		"message": fmt.Sprintf("Message delivered to %s.", to),
	})
}

// Static assertion: SendMessageTool implements agentcore.Tool.
var _ agentcore.Tool = (*SendMessageTool)(nil)
