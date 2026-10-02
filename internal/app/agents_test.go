package app

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

func TestEventHub_DeliversToSubscribers(t *testing.T) {
	h := NewAgentHub()
	_, ch, cancel := h.Subscribe("researcher")
	defer cancel()

	h.Publish("researcher", agentcore.MessageStart{})
	h.Publish("researcher", agentcore.ToolStart{})
	h.Publish("researcher", agentcore.RunEnd{})

	got := drainEvents(t, ch, 3, time.Second)
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(got), got)
	}
	if got, want := types(got), "agentcore.MessageStart agentcore.ToolStart agentcore.RunEnd"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestEventHub_RoutesByAgentName(t *testing.T) {
	h := NewAgentHub()
	_, chA, cancelA := h.Subscribe("alice")
	defer cancelA()
	_, chB, cancelB := h.Subscribe("bob")
	defer cancelB()

	h.Publish("alice", agentcore.MessageStart{})
	h.Publish("bob", agentcore.ToolStart{})

	gotA := drainEvents(t, chA, 1, time.Second)
	gotB := drainEvents(t, chB, 1, time.Second)
	if types(gotA) != "agentcore.MessageStart" {
		t.Errorf("alice got %+v, want AgentStart", gotA)
	}
	if types(gotB) != "agentcore.ToolStart" {
		t.Errorf("bob got %+v, want ToolExecStart", gotB)
	}
}

func TestEventHub_UnsubscribeStopsDelivery(t *testing.T) {
	h := NewAgentHub()
	_, ch, cancel := h.Subscribe("researcher")

	h.Publish("researcher", agentcore.MessageStart{})
	cancel()

	// After cancel, the channel must be closed; any read returns zero/false.
	select {
	case _, ok := <-ch:
		if ok {
			// Drain the AgentStart that was already buffered; the close
			// must come on the next read.
			select {
			case _, ok2 := <-ch:
				if ok2 {
					t.Error("channel still open after unsubscribe")
				}
			case <-time.After(time.Second):
				t.Error("channel did not close within 1s")
			}
		}
	case <-time.After(time.Second):
		t.Error("channel did not close within 1s")
	}

	// Subsequent publishes must not panic — they should just not route to us.
	h.Publish("researcher", agentcore.RunEnd{})
}

// Slow consumers must not stall the publisher. We fill the buffer, publish
// many more events, and assert Publish returns quickly each time.
func TestEventHub_NonBlockingOnSlowConsumer(t *testing.T) {
	h := NewAgentHub()
	_, _, cancel := h.Subscribe("researcher") // never read from it
	defer cancel()

	start := time.Now()
	for range subBufferSize * 4 {
		h.Publish("researcher", agentcore.MessageStart{})
	}
	elapsed := time.Since(start)
	// Concrete budget: 4×buffer publishes against a deadlocked consumer
	// should be well under 100ms — anything close to a second means we are
	// blocking.
	if elapsed > 100*time.Millisecond {
		t.Errorf("publishing took %v with slow consumer; expected non-blocking", elapsed)
	}
}

// Concurrent publishers + subscribers must not race or deadlock. Race
// detector (`go test -race`) catches the rest; this test just exercises the
// scheduling.
func TestEventHub_Concurrent(t *testing.T) {
	h := NewAgentHub()
	var wg sync.WaitGroup

	// 4 subscribers consuming in tight loops.
	for range 4 {
		_, ch, cancel := h.Subscribe("a")
		wg.Go(func() {
			for range ch {
				// drain
			}
		})
		time.AfterFunc(200*time.Millisecond, cancel)
	}

	// 8 publishers writing concurrently.
	for range 8 {
		wg.Go(func() {
			for range 1000 {
				h.Publish("a", agentcore.MessageStart{})
			}
		})
	}
	wg.Wait()
}

func TestEventHub_ActiveAgentsReflectsState(t *testing.T) {
	h := NewAgentHub()
	if got := h.ActiveAgents(); len(got) != 0 {
		t.Errorf("initial ActiveAgents = %v, want empty", got)
	}
	h.Publish("alice", agentcore.MessageStart{})
	h.Publish("bob", agentcore.MessageStart{})
	got := h.ActiveAgents()
	if len(got) != 2 {
		t.Errorf("ActiveAgents = %v, want 2 entries", got)
	}
	h.MarkStopped("alice")
	got = h.ActiveAgents()
	if len(got) != 1 || got[0] != "bob" {
		t.Errorf("ActiveAgents after stop = %v, want [bob]", got)
	}
}

func TestEventHub_LateSubscriberReplaysHistory(t *testing.T) {
	h := NewAgentHub()
	// Publish a few events BEFORE any subscriber attaches — these must be
	// replayed when Subscribe is called.
	h.Publish("alice", agentcore.MessageStart{})
	h.Publish("alice", agentcore.ToolStart{})
	h.Publish("alice", agentcore.RunEnd{})

	history, ch, cancel := h.Subscribe("alice")
	defer cancel()

	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3: %+v", len(history), history)
	}
	if got, want := types(history), "agentcore.MessageStart agentcore.ToolStart agentcore.RunEnd"; got != want {
		t.Errorf("history %s, want %s", got, want)
	}

	// Subsequent live events still arrive on the channel, picking up where
	// history left off — no duplicates.
	h.Publish("alice", agentcore.Retry{})
	live := drainEvents(t, ch, 1, time.Second)
	if types(live) != "agentcore.Retry" {
		t.Errorf("live events = %+v, want [Retry]", live)
	}
}

func TestEventHub_HistorySurvivesMarkStopped(t *testing.T) {
	h := NewAgentHub()
	h.Publish("alice", agentcore.MessageStart{})
	h.Publish("alice", agentcore.RunEnd{})
	h.MarkStopped("alice")

	// Subscribing to a stopped agent still yields the recorded history.
	history, ch, cancel := h.Subscribe("alice")
	defer cancel()
	if len(history) != 2 {
		t.Errorf("history after stop = %d events, want 2: %+v", len(history), history)
	}
	// No live events should arrive — alice is no longer publishing.
	select {
	case ev := <-ch:
		t.Errorf("unexpected live event after stop: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEventHub_HistoryRingTruncatesOldest(t *testing.T) {
	h := NewAgentHub()
	// Publish capacity+10 events; the ring should retain only the last
	// `historyCapacity` of them in chronological order.
	const overshoot = 10
	for range historyCapacity + overshoot {
		h.Publish("alice", agentcore.ToolStart{})
	}
	history, _, cancel := h.Subscribe("alice")
	defer cancel()
	if len(history) != historyCapacity {
		t.Errorf("history len = %d, want %d (overshoot dropped)", len(history), historyCapacity)
	}
}

func TestEventHub_KnownAgentsIncludesStopped(t *testing.T) {
	h := NewAgentHub()
	h.Publish("alice", agentcore.MessageStart{})
	h.Publish("bob", agentcore.MessageStart{})
	h.MarkStopped("alice")

	known := h.KnownAgents()
	if len(known) != 2 {
		t.Fatalf("KnownAgents = %+v, want 2 entries", known)
	}
	gotActive := map[string]bool{}
	for _, info := range known {
		gotActive[info.Name] = info.Active
	}
	if gotActive["alice"] {
		t.Errorf("alice reported active after MarkStopped")
	}
	if !gotActive["bob"] {
		t.Errorf("bob should still be active")
	}

	// IsActive mirrors the per-name flag.
	if h.IsActive("alice") {
		t.Errorf("IsActive(alice) = true, want false")
	}
	if !h.IsActive("bob") {
		t.Errorf("IsActive(bob) = false, want true")
	}
	if h.IsActive("eve") {
		t.Errorf("IsActive(unknown) should be false")
	}
}

// --- helpers -----------------------------------------------------------------

func drainEvents(t *testing.T, ch <-chan agentcore.Event, n int, timeout time.Duration) []agentcore.Event {
	t.Helper()
	out := make([]agentcore.Event, 0, n)
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
	return out
}

// types names the types of evs.
func types(evs []agentcore.Event) string {
	names := make([]string, len(evs))
	for i, ev := range evs {
		names[i] = fmt.Sprintf("%T", ev)
	}
	return strings.Join(names, " ")
}
