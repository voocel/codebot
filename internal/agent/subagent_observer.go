package agent

import (
	"fmt"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
)

// SubagentHubObserver builds the subagent.Tool event observer that fans every
// sub-agent run's raw AgentLoop events into the teammate event hub. This is what
// makes one-shot / background sub-agents observable in the live-preview modal
// the same way long-lived teammates already are — one hub, two producers.
//
// Naming: the hub keys by a human-readable display name, but RunMeta.Agent is
// the agent TYPE (e.g. "explore"), which collides when the same type runs
// concurrently (parallel mode) or alongside a same-named teammate. We assign
// each unique RunMeta.InstanceID a display name on its first event, picking the
// bare type when free and appending " #2", " #3", … otherwise — dedup mirrors
// uniqueAgentName but resolves against names currently live in the hub plus the
// ones this observer has already handed out. The mapping is released on the
// run's EventAgentEnd (guaranteed on every termination path) so names recycle.
//
// Returns nil when hub is nil so callers can wire unconditionally.
func SubagentHubObserver(hub *TeammateEventHub) func(meta subagent.RunMeta, ev agentcore.Event) {
	if hub == nil {
		return nil
	}
	var mu sync.Mutex
	assigned := make(map[string]string) // InstanceID → displayName

	return func(meta subagent.RunMeta, ev agentcore.Event) {
		// MVP scope: only background runs are routed to the hub. Foreground
		// single/parallel/chain already stream inline in the leader transcript;
		// publishing them here too would flood the modal's known-agent list with
		// many short-lived entries and grow the hub's retained history unbounded.
		// The kernel observer stays mode-agnostic; this harness policy filters.
		// Lifting it to cover foreground (esp. parallel isolation) is the planned
		// next increment — see tasks/subagent-live-preview.md.
		if meta.Mode != subagent.ModeBackground {
			return
		}

		mu.Lock()
		name, ok := assigned[meta.InstanceID]
		if !ok {
			name = uniqueHubName(hub, meta.Agent, assigned)
			assigned[meta.InstanceID] = name
		}
		mu.Unlock()

		hub.Publish(name, ev)

		if ev.Type == agentcore.EventAgentEnd {
			hub.MarkStopped(name)
			mu.Lock()
			delete(assigned, meta.InstanceID)
			mu.Unlock()
		}
	}
}

// uniqueHubName returns base when no live hub agent and no already-assigned
// sub-agent uses it; otherwise appends " #2", " #3", … Caller holds the
// observer mutex so concurrent first-events serialise and can't pick the same
// name. taken reflects sub-agent names this observer manages; hub.ActiveAgents
// covers teammates + already-publishing sub-agents.
func uniqueHubName(hub *TeammateEventHub, base string, assigned map[string]string) string {
	if base == "" {
		base = "subagent"
	}
	taken := make(map[string]bool)
	for _, n := range hub.ActiveAgents() {
		taken[n] = true
	}
	for _, n := range assigned {
		taken[n] = true
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s #%d", base, i)
		if !taken[cand] {
			return cand
		}
	}
}
