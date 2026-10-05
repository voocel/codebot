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
// the agent type (e.g. "explore"), shared by every run of it. Each background
// run gets the bare type when no earlier run had it, else the type with
// " #2", " #3", …: the hub keeps a finished run's events, which a later run
// under its name would add to.
func agentEmit(hub *AgentHub) func(coresub.Spawn) func(agentcore.Event) error {
	var mu sync.Mutex
	taken := make(map[string]bool) // names of the background runs so far
	return func(s coresub.Spawn) func(agentcore.Event) error {
		if s.Mode != coresub.ModeBackground {
			return nil
		}
		mu.Lock()
		name := uniqueHubName(s.Agent, taken)
		taken[name] = true
		mu.Unlock()
		return func(ev agentcore.Event) error {
			hub.Publish(name, ev)
			if _, ok := ev.(agentcore.RunEnd); ok {
				hub.MarkStopped(name)
			}
			return nil
		}
	}
}

// uniqueHubName returns base when it is not taken; otherwise base with
// " #2", " #3", …
func uniqueHubName(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		if name := fmt.Sprintf("%s #%d", base, i); !taken[name] {
			return name
		}
	}
}
