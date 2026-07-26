package team

import (
	"slices"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
)

// Helpers emit background-mode meta — the only mode the observer currently
// routes to the hub (foreground already streams inline; see SubagentHubObserver).
func start(agentType, instanceID string) (subagent.RunMeta, agentcore.Event) {
	return subagent.RunMeta{Agent: agentType, InstanceID: instanceID, Mode: subagent.ModeBackground},
		agentcore.Event{Type: agentcore.EventAgentStart}
}

func end(agentType, instanceID string) (subagent.RunMeta, agentcore.Event) {
	return subagent.RunMeta{Agent: agentType, InstanceID: instanceID, Mode: subagent.ModeBackground},
		agentcore.Event{Type: agentcore.EventAgentEnd}
}

// Two concurrent runs of the same agent type must land on distinct hub names so
// the preview modal can show each separately.
func TestSubagentHubObserver_ParallelSameTypeDisambiguates(t *testing.T) {
	hub := NewEventHub()
	obs := SubagentHubObserver(hub)

	obs(start("explore", "a"))
	obs(start("explore", "b"))

	active := hub.ActiveAgents()
	slices.Sort(active)
	want := []string{"explore", "explore #2"}
	if !slices.Equal(active, want) {
		t.Fatalf("active agents = %v, want %v", active, want)
	}
}

// A name freed by EventAgentEnd is reusable by a later run.
func TestSubagentHubObserver_ReleasesNameOnEnd(t *testing.T) {
	hub := NewEventHub()
	obs := SubagentHubObserver(hub)

	obs(start("explore", "a"))
	if got := hub.ActiveAgents(); !slices.Equal(got, []string{"explore"}) {
		t.Fatalf("after start: active = %v", got)
	}

	obs(end("explore", "a"))
	if got := hub.ActiveAgents(); len(got) != 0 {
		t.Fatalf("after end: active = %v, want empty", got)
	}

	// A fresh run reuses the now-free bare name rather than "explore #2".
	obs(start("explore", "c"))
	if got := hub.ActiveAgents(); !slices.Equal(got, []string{"explore"}) {
		t.Fatalf("after reuse: active = %v, want [explore]", got)
	}
}

// A sub-agent must not collide with a same-named teammate already in the hub.
func TestSubagentHubObserver_AvoidsTeammateName(t *testing.T) {
	hub := NewEventHub()
	// Simulate an active teammate named "explore" (teammate path publishes
	// directly under its name).
	hub.Publish("explore", agentcore.Event{Type: agentcore.EventAgentStart})

	obs := SubagentHubObserver(hub)
	obs(start("explore", "x"))

	active := hub.ActiveAgents()
	slices.Sort(active)
	want := []string{"explore", "explore #2"}
	if !slices.Equal(active, want) {
		t.Fatalf("active agents = %v, want %v", active, want)
	}
}

// Foreground modes already stream inline; they must not reach the hub.
func TestSubagentHubObserver_SkipsForegroundModes(t *testing.T) {
	for _, mode := range []string{"single", "parallel", "chain"} {
		hub := NewEventHub()
		obs := SubagentHubObserver(hub)
		obs(subagent.RunMeta{Agent: "explore", InstanceID: "a", Mode: mode},
			agentcore.Event{Type: agentcore.EventAgentStart})
		if got := hub.KnownAgents(); len(got) != 0 {
			t.Errorf("mode %q: expected no hub agents, got %v", mode, got)
		}
	}
}

func TestSubagentHubObserver_NilHub(t *testing.T) {
	if SubagentHubObserver(nil) != nil {
		t.Fatal("nil hub must yield nil observer")
	}
}
