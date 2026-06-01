package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/agentcore/team"
	cbteam "github.com/voocel/codebot/internal/team"
)

// TeamDismissTool is the leader's "stop a teammate gracefully" surface. It
// writes a top-priority shutdown_request envelope to the teammate's mailbox;
// the teammate's runner picks it up at the next turn boundary and exits
// without consulting the model. The teammate's Entry status moves to
// Completed; its mailbox is closed by the spawner's defer.
//
// Why a dedicated tool instead of folding into send_message:
//   - send_message takes plain text; this is a structured control message.
//     Asking the LLM to hand-format JSON envelopes inside send_message is
//     fragile and easy to get wrong.
//   - team_dismiss is leader-only (filtered out for all sub-agents via
//     allAgentDisallowed). send_message stays available to teammates.
//
// For hard abort (teammate stuck, no graceful path), the equivalent action
// is task_stop on the teammate's task ID — that cancels the runner's ctx
// and Run exits via ctx.Err() instead of message handling.
type TeamDismissTool struct {
	reg       *team.Registry
	onDismiss func(name string) // optional: drop the teammate from the persisted roster
}

// NewTeamDismissTool wires the tool to the session's team registry. A nil
// registry is a programmer error.
func NewTeamDismissTool(reg *team.Registry) *TeamDismissTool {
	return &TeamDismissTool{reg: reg}
}

// SetRosterRemover registers a callback fired when a teammate is dismissed, so
// the persisted roster drops it and a later session resume does not resurrect
// a deliberately-retired teammate. Optional; nil disables roster cleanup.
func (t *TeamDismissTool) SetRosterRemover(fn func(name string)) {
	t.onDismiss = fn
}

func (t *TeamDismissTool) Name() string  { return "team_dismiss" }
func (t *TeamDismissTool) Label() string { return "Dismiss Teammate" }

func (t *TeamDismissTool) Description() string {
	return `Gracefully dismiss a teammate from the active team. The teammate finishes its current turn (if running), receives the shutdown signal at its next turn boundary, and exits cleanly — no model decision is consulted.

Use this when a teammate has completed its purpose and no further work is needed, or when you want to free a name slot for a different teammate. For an unresponsive teammate, use task_stop on its task ID instead (hard cancel).`
}

func (t *TeamDismissTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("name", schema.String(
			"Teammate's name in the team (the value used for `name` when spawning). 'team-lead' cannot be dismissed.",
		)).Required(),
		schema.Property("reason", schema.String(
			"Short reason recorded for the transcript (not shown to the teammate's model).",
		)),
	)
}

func (t *TeamDismissTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return json.Marshal("Validation error: name is required")
	}
	if name == team.TeamLeadName {
		return json.Marshal(fmt.Sprintf("Cannot dismiss %q — the leader is the team owner. Use /team delete to tear down the whole team.", team.TeamLeadName))
	}
	if !t.reg.HasTeam() {
		return json.Marshal("No active team in this session.")
	}
	mb := t.reg.Mailbox(name)
	if mb == nil {
		return json.Marshal(fmt.Sprintf("No teammate named %q in the active team.", name))
	}

	err := mb.Send(team.Message{
		From: team.TeamLeadName,
		Text: cbteam.EncodeShutdownRequest(strings.TrimSpace(a.Reason)),
	})
	if err != nil {
		if errors.Is(err, team.ErrClosed) {
			return json.Marshal(fmt.Sprintf("Teammate %q has already shut down.", name))
		}
		return nil, fmt.Errorf("send shutdown to %s: %w", name, err)
	}
	// Drop from the persisted roster so a later resume does not resurrect a
	// teammate the leader deliberately retired. Done on dismiss intent (not on
	// the async exit) so the roster reflects the leader's decision immediately.
	if t.onDismiss != nil {
		t.onDismiss(name)
	}
	return json.Marshal(map[string]any{
		"success": true,
		"message": fmt.Sprintf("Shutdown request queued for %q; the teammate will exit at its next turn boundary.", name),
	})
}

// Static assertion: TeamDismissTool implements agentcore.Tool.
var _ agentcore.Tool = (*TeamDismissTool)(nil)
