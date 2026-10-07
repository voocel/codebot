package app

import (
	"fmt"
	"sync"

	"github.com/voocel/agentcore"
	coresub "github.com/voocel/agentcore/subagent"
)

// agentEmit sends a background run's events to hub so the TUI can list and
// stream them. A foreground run needs none: its events stream inline as the
// subagent call's progress.
//
// Spawn.Agent is the agent type (e.g. "explore"), shared by all its runs. The
// hub keeps a finished run's events, so each background run gets its own
// name: the bare type first, then " #2", " #3", …
func agentEmit(hub *AgentHub) func(coresub.Spawn) func(agentcore.Event) error {
	var mu sync.Mutex
	taken := make(map[string]bool)
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
