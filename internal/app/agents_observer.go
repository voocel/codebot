package app

import (
	"github.com/voocel/agentcore"
	coresub "github.com/voocel/agentcore/subagent"
)

// agentEmit sends a background run's events to hub so the TUI can list and
// stream them, under the run's Spawn.ID, which its task records as Run. A
// foreground run needs none: its events stream inline as the subagent call's
// progress.
func agentEmit(hub *AgentHub) func(coresub.Spawn) func(agentcore.Event) error {
	return func(s coresub.Spawn) func(agentcore.Event) error {
		if s.Mode != coresub.ModeBackground {
			return nil
		}
		return func(ev agentcore.Event) error {
			hub.Publish(s.ID, ev)
			if _, ok := ev.(agentcore.RunEnd); ok {
				hub.MarkStopped(s.ID)
			}
			return nil
		}
	}
}
