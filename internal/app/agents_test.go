package app

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

// Unsubscribing closes the channel, and a later publish does not send on it.
func TestEventHub_UnsubscribeStopsDelivery(t *testing.T) {
	h := NewAgentHub()
	_, ch, cancel := h.Subscribe("researcher")

	h.Publish("researcher", agentcore.MessageStart{})
	cancel()

	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("channel did not close within 1s")
	}

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

// A late subscriber gets the history, oldest first, then the live events,
// none twice. The history keeps the messages, not the deltas they streamed
// as.
func TestEventHub_LateSubscriberReplaysHistory(t *testing.T) {
	h := NewAgentHub()
	// Publish a few events BEFORE any subscriber attaches — these must be
	// replayed when Subscribe is called, the delta left out.
	h.Publish("alice", agentcore.MessageStart{})
	h.Publish("alice", agentcore.MessageDelta{})
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
