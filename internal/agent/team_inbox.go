package agent

import (
	"context"
	"errors"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/team"
	cbteam "github.com/voocel/codebot/internal/team"
)

// MessageInjector is the slice of *agentcore.Agent the pump actually uses.
// Defined as an interface so tests can stand in a fake without spinning the
// full agent machinery.
type MessageInjector interface {
	Inject(context.Context, agentcore.AgentMessage) (agentcore.InjectResult, error)
}

// LeaderInboxPump bridges the leader's mailbox to the leader agent. Without
// it, messages routed by send_message to "team-lead" land in the mailbox but
// never reach the model — there is no equivalent of the teammate runner loop
// for the main agent (it is driven by the user / TUI session instead).
//
// The pump:
//   - sleeps in short backoff while no team is active (team_create wires up
//     the leader mailbox only when invoked);
//   - subscribes to the leader mailbox via Wait and drains arriving messages;
//   - filters out idle_notification envelopes (they exist to wake the leader
//     for fan-out coordination, but the model itself should not see the JSON);
//   - calls Agent.Inject for every remaining message so the agent steers /
//     resumes / queues based on its current state.
//
// Lifecycle: spawned at boot, exits when ctx is cancelled (Runtime.Close).
type LeaderInboxPump struct {
	reg          *team.Registry
	agent        MessageInjector
	waitInterval time.Duration
}

// defaultPumpWaitInterval bounds team_create → first-delivery latency. Short
// enough to feel instant; long enough to keep the idle goroutine cheap.
const defaultPumpWaitInterval = 200 * time.Millisecond

// NewLeaderInboxPump constructs a pump for the given registry + leader agent.
// A zero waitInterval picks the default. Both reg and ag must be non-nil.
func NewLeaderInboxPump(reg *team.Registry, ag MessageInjector, waitInterval time.Duration) *LeaderInboxPump {
	if waitInterval <= 0 {
		waitInterval = defaultPumpWaitInterval
	}
	return &LeaderInboxPump{reg: reg, agent: ag, waitInterval: waitInterval}
}

// Run blocks until ctx is cancelled. Safe to call as `go pump.Run(ctx)` from
// bootstrap. The loop is two-phase:
//
//  1. No team yet: short timer-driven backoff until Registry.Mailbox returns
//     non-nil for TeamLeadName.
//  2. Team active: Wait on the mailbox, Drain on wake, Inject each non-control
//     message. On ErrClosed (team torn down) fall back to phase 1.
func (p *LeaderInboxPump) Run(ctx context.Context) {
	for {
		if err := ctx.Err(); err != nil {
			return
		}

		mb := p.reg.Mailbox(team.TeamLeadName)
		if mb == nil {
			if !p.sleep(ctx) {
				return
			}
			continue
		}

		if err := mb.Wait(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			// ErrClosed: team was deleted between two iterations. Loop and
			// the next Mailbox() call will return nil → backoff sleep until
			// a new team is created (Stage D will allow team recreation).
			continue
		}

		for _, m := range mb.Drain() {
			text, ok := pumpDecodeBody(m)
			if !ok {
				continue
			}
			attachment := cbteam.FormatTeammateAttachment(m.From, text, m.Color, m.Summary)
			// Inject only fails on a nil message or while a Reset/SwitchSession
			// holds the run lifecycle (ErrRunsHeld, nothing queued). Dropping
			// the drained message then matches the surgery's own semantics —
			// its ClearAllQueues would wipe a queued copy anyway. Background
			// ctx: a resumed run must outlive this pump's drain cycle.
			_, _ = p.agent.Inject(context.Background(), agentcore.UserMsg(attachment))
		}
	}
}

// pumpDecodeBody normalises an inbox message into a body the leader's model
// should see. Three cases:
//
//   - idle_notification with text: the teammate's last assistant content. We
//     surface it as a normal peer DM so the leader's model can react.
//   - idle_notification without text: tool-only turn (no model output worth
//     repeating). Inject a short status line so the leader still knows the
//     teammate is idle and ready for the next instruction.
//   - shutdown_request: a control envelope that should never reach the leader
//     today (team_dismiss only sends it leader → teammate, not the other way).
//     Drop defensively if it does — exposing raw JSON to the model would
//     leak protocol internals into the prompt. Stage E will route inbound
//     control messages through a typed handler instead.
//   - anything else: pass through verbatim (plain peer text).
//
// Returns ok=false when the message must be silently consumed.
func pumpDecodeBody(m team.Message) (string, bool) {
	if cbteam.IsIdleNotification(m.Text) {
		if text := cbteam.IdleNotificationText(m.Text); text != "" {
			return text, true
		}
		return cbteam.FallbackIdleStatus(m.From), true
	}
	if cbteam.IsShutdownRequest(m.Text) {
		return "", false
	}
	return m.Text, true
}

// sleep blocks for waitInterval or until ctx is cancelled. Returns false if
// ctx fired first so the caller can exit the outer loop.
func (p *LeaderInboxPump) sleep(ctx context.Context) bool {
	t := time.NewTimer(p.waitInterval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
