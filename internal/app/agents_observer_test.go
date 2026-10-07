package app

import (
	"slices"
	"testing"

	"github.com/voocel/agentcore"
	coresub "github.com/voocel/agentcore/subagent"
)

func background(agent string) coresub.Spawn {
	return coresub.Spawn{Agent: agent, Mode: coresub.ModeBackground}
}

// A finished run keeps its name, so a later run of the same type gets a new
// one. A foreground run never reaches the hub.
func TestAgentEmit_KeepsFinishedRunsApart(t *testing.T) {
	hub := NewAgentHub()
	emit := agentEmit(hub)

	if emit(coresub.Spawn{Agent: "explore", Mode: coresub.ModeSingle}) != nil {
		t.Fatal("a foreground run reaches the hub")
	}
	run := emit(background("explore"))
	_ = run(agentcore.MessageStart{})
	_ = run(agentcore.RunEnd{})
	if got := hub.ActiveAgents(); len(got) != 0 {
		t.Fatalf("after end: active = %v, want empty", got)
	}

	_ = emit(background("explore"))(agentcore.MessageStart{})
	if got := hub.ActiveAgents(); !slices.Equal(got, []string{"explore #2"}) {
		t.Fatalf("second run: active = %v, want [explore #2]", got)
	}
	history, _, cancel := hub.Subscribe("explore")
	defer cancel()
	if len(history) != 2 {
		t.Fatalf("first run's history = %#v, want its own two events", history)
	}
}
