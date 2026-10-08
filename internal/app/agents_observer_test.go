package app

import (
	"slices"
	"testing"

	"github.com/voocel/agentcore"
	coresub "github.com/voocel/agentcore/subagent"
)

// Each background run is on the hub under its Spawn.ID, which its task
// records as Run, so finished runs stay apart. A foreground run never reaches
// the hub.
func TestAgentEmit_KeepsRunsApart(t *testing.T) {
	hub := NewAgentHub()
	emit := agentEmit(hub)

	if emit(coresub.Spawn{Agent: "explore", ID: "explore#1", Mode: coresub.ModeSingle}) != nil {
		t.Fatal("a foreground run reaches the hub")
	}
	run := emit(coresub.Spawn{Agent: "explore", ID: "explore#2", Mode: coresub.ModeBackground})
	_ = run(agentcore.MessageStart{})
	_ = run(agentcore.RunEnd{})
	if got := hub.ActiveAgents(); len(got) != 0 {
		t.Fatalf("after end: active = %v, want empty", got)
	}

	_ = emit(coresub.Spawn{Agent: "explore", ID: "explore#3", Mode: coresub.ModeBackground})(agentcore.MessageStart{})
	if got := hub.ActiveAgents(); !slices.Equal(got, []string{"explore#3"}) {
		t.Fatalf("second run: active = %v, want [explore#3]", got)
	}
	history, _, cancel := hub.Subscribe("explore#2")
	defer cancel()
	if len(history) != 2 {
		t.Fatalf("first run's history = %#v, want its own two events", history)
	}
}
