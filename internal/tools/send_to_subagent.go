package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/agentcore/task"
)

// SendToSubAgentTool queues a user-style message to a background sub-agent's
// next turn. The sub-agent's loop drains it through GetSteeringMessages, so
// the message becomes visible at the next steering tick (turn boundary or
// post-tool) rather than waiting for the agent to decide to stop.
//
// This is the parent→child runtime communication path. A sub-agent CANNOT use
// this tool — when team support lands it will get its own peer-messaging
// tool, and send_to_subagent stays strictly parent→child to keep the channels
// distinct. tool_filter.go enforces the exclusion.
//
// Terminal tasks are NOT auto-resumed. CC resumes from on-disk transcripts;
// that's a stage 6 capability. Today we report the failure verbatim so the
// model can tell the user the message was not delivered.
type SendToSubAgentTool struct {
	rt *task.Runtime
}

// NewSendToSubAgentTool constructs the tool. nil rt is a programmer error —
// the tool is meaningless without a runtime to look tasks up in.
func NewSendToSubAgentTool(rt *task.Runtime) *SendToSubAgentTool {
	return &SendToSubAgentTool{rt: rt}
}

func (t *SendToSubAgentTool) Name() string  { return "send_to_subagent" }
func (t *SendToSubAgentTool) Label() string { return "Send Message to SubAgent" }

func (t *SendToSubAgentTool) Description() string {
	return `Send a follow-up message to a running background sub-agent. The message is delivered as a user turn at the sub-agent's next tool-round boundary — it will see and respond to it without waiting to finish its current work.

Use this when you need to:
- correct or extend the sub-agent's instructions mid-flight
- relay user feedback that arrived after you launched the background task
- provide information the sub-agent asked for via its progress output

The target task must still be running. If it has completed/failed/killed, this tool returns an error — start a new sub-agent instead.`
}

func (t *SendToSubAgentTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("task_id", schema.String("ID of the background sub-agent task (from the subagent tool's response)")).Required(),
		schema.Property("message", schema.String("Plain text message delivered as a user turn at the sub-agent's next tool-round boundary")).Required(),
	)
}

func (t *SendToSubAgentTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		TaskID  string `json:"task_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.TaskID) == "" {
		return json.Marshal("Validation error: task_id is required")
	}
	if strings.TrimSpace(a.Message) == "" {
		return json.Marshal("Validation error: message is required")
	}

	// Reject non-subagent targets explicitly. AppendPending doesn't care about
	// type, but routing a message into a shell task's queue would silently no-op
	// — the shell goroutine never drains it. Better to fail loudly here.
	entry := t.rt.Get(a.TaskID)
	if entry == nil {
		return json.Marshal(fmt.Sprintf("Task %s not found", a.TaskID))
	}
	if entry.Type != task.TypeSubAgent {
		return json.Marshal(fmt.Sprintf("Task %s is type %q; send_to_subagent only delivers to subagent tasks", a.TaskID, entry.Type))
	}

	switch t.rt.AppendPending(a.TaskID, a.Message) {
	case task.AppendOK:
		return json.Marshal(map[string]any{
			"success": true,
			"message": fmt.Sprintf("Message queued for task %s (%s); it will be delivered at the sub-agent's next tool-round boundary.", a.TaskID, entry.Agent),
		})
	case task.AppendNotFound:
		// Lost the race: Get returned an entry but it was evicted between calls.
		// Treat the same as the initial not-found branch.
		return json.Marshal(fmt.Sprintf("Task %s no longer exists", a.TaskID))
	case task.AppendTerminal:
		return json.Marshal(map[string]any{
			"success": false,
			"message": fmt.Sprintf("Task %s has already finished (status=%s); cannot deliver the message. Start a new sub-agent instead.", a.TaskID, entry.Status),
		})
	}
	// Unreachable — switch above is exhaustive on AppendStatus.
	return json.Marshal("internal error: unexpected AppendStatus")
}
