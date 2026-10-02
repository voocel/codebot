package app

import (
	"fmt"
	"sync"

	"github.com/voocel/agentcore"
	coresub "github.com/voocel/agentcore/subagent"
)

// agentEmit returns where the events of a sub-agent run go: a background
// run's to hub, so the TUI can list and stream them; a foreground run's
// nowhere, since they stream inline as the subagent call's progress.
//
// Naming: the hub keys by a human-readable display name, but Spawn.Agent is
// the agent type (e.g. "explore"), which collides when the same type runs
// concurrently. Each background run gets the bare type when no live run has
// it, else the type with " #2", " #3", …; its RunEnd releases the name.
func agentEmit(hub *AgentHub) func(coresub.Spawn) func(agentcore.Event) error {
	var mu sync.Mutex
	live := make(map[string]bool) // names of the background runs under way
	return func(s coresub.Spawn) func(agentcore.Event) error {
		if s.Mode != coresub.ModeBackground {
			return nil
		}
		mu.Lock()
		name := uniqueHubName(s.Agent, live)
		live[name] = true
		mu.Unlock()
		return func(ev agentcore.Event) error {
			hub.Publish(name, ev)
			if _, ok := ev.(agentcore.RunEnd); ok {
				hub.MarkStopped(name)
				mu.Lock()
				delete(live, name)
				mu.Unlock()
			}
			return nil
		}
	}
}

// uniqueHubName returns base when no live run has it; otherwise base with
// " #2", " #3", …
func uniqueHubName(base string, live map[string]bool) string {
	if !live[base] {
		return base
	}
	for i := 2; ; i++ {
		if name := fmt.Sprintf("%s #%d", base, i); !live[name] {
			return name
		}
	}
}
