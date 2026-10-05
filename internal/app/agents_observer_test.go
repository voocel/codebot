package app

import (
	"slices"
	"testing"

	"github.com/voocel/agentcore"
	coresub "github.com/voocel/agentcore/subagent"
	"github.com/voocel/litellm"
)

func background(agent string) coresub.Spawn {
	return coresub.Spawn{Agent: agent, Mode: coresub.ModeBackground}
}

// Two concurrent runs of the same agent type must land on distinct hub names so
// the preview modal can show each separately.
func TestAgentEmit_ParallelSameTypeDisambiguates(t *testing.T) {
	hub := NewAgentHub()
	emit := agentEmit(hub)

	_ = emit(background("explore"))(agentcore.MessageStart{})
	_ = emit(background("explore"))(agentcore.MessageStart{})

	active := hub.ActiveAgents()
	slices.Sort(active)
	want := []string{"explore", "explore #2"}
	if !slices.Equal(active, want) {
		t.Fatalf("active agents = %v, want %v", active, want)
	}
}

// A finished run keeps its name, so a later run of the same type does not
// add to its transcript.
func TestAgentEmit_KeepsFinishedRunsApart(t *testing.T) {
	hub := NewAgentHub()
	emit := agentEmit(hub)

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

// Foreground modes already stream inline; they must not reach the hub.
func TestAgentEmit_SkipsForegroundModes(t *testing.T) {
	emit := agentEmit(NewAgentHub())
	for _, mode := range []coresub.Mode{coresub.ModeSingle, coresub.ModeParallel, coresub.ModeChain} {
		if emit(coresub.Spawn{Agent: "explore", Mode: mode}) != nil {
			t.Errorf("mode %q reaches the hub", mode)
		}
	}
}

// The replay ring keeps the messages, not the deltas they streamed as.
func TestAgentHubKeepsNoDeltas(t *testing.T) {
	hub := NewAgentHub()
	hub.Publish("a", agentcore.MessageDelta{Event: litellm.TextDelta{Text: "hi"}})
	hub.Publish("a", agentcore.MessageEnd{Message: agentcore.UserText("hi")})
	history, _, cancel := hub.Subscribe("a")
	defer cancel()
	if len(history) != 1 {
		t.Fatalf("history = %#v", history)
	}
}
