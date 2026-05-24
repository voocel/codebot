package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

func TestEventHub_NilReceiverIsNoop(t *testing.T) {
	var h *TeammateEventHub
	// Each of these would panic if the nil-check were missing.
	h.Publish("x", agentcore.Event{Type: agentcore.EventAgentStart})
	h.MarkStopped("x")
	if got := h.ActiveAgents(); got != nil {
		t.Errorf("ActiveAgents on nil = %v, want nil", got)
	}
	ch, cancel := h.Subscribe("x")
	if _, ok := <-ch; ok {
		t.Error("Subscribe on nil hub returned a non-closed channel")
	}
	cancel()
	pch, pcancel := h.SubscribePresence()
	if _, ok := <-pch; ok {
		t.Error("SubscribePresence on nil hub returned a non-closed channel")
	}
	pcancel()
}

func TestEventHub_DeliversToSubscribers(t *testing.T) {
	h := NewTeammateEventHub()
	ch, cancel := h.Subscribe("researcher")
	defer cancel()

	h.Publish("researcher", agentcore.Event{Type: agentcore.EventAgentStart})
	h.Publish("researcher", agentcore.Event{Type: agentcore.EventToolExecStart})
	h.Publish("researcher", agentcore.Event{Type: agentcore.EventAgentEnd})

	got := drainEvents(t, ch, 3, time.Second)
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(got), got)
	}
	want := []agentcore.EventType{
		agentcore.EventAgentStart,
		agentcore.EventToolExecStart,
		agentcore.EventAgentEnd,
	}
	for i, ev := range got {
		if ev.Type != want[i] {
			t.Errorf("got[%d] = %v, want %v", i, ev.Type, want[i])
		}
	}
}

func TestEventHub_RoutesByAgentName(t *testing.T) {
	h := NewTeammateEventHub()
	chA, cancelA := h.Subscribe("alice")
	defer cancelA()
	chB, cancelB := h.Subscribe("bob")
	defer cancelB()

	h.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})
	h.Publish("bob", agentcore.Event{Type: agentcore.EventToolExecStart})

	gotA := drainEvents(t, chA, 1, time.Second)
	gotB := drainEvents(t, chB, 1, time.Second)
	if len(gotA) != 1 || gotA[0].Type != agentcore.EventAgentStart {
		t.Errorf("alice got %+v, want AgentStart", gotA)
	}
	if len(gotB) != 1 || gotB[0].Type != agentcore.EventToolExecStart {
		t.Errorf("bob got %+v, want ToolExecStart", gotB)
	}
}

func TestEventHub_UnsubscribeStopsDelivery(t *testing.T) {
	h := NewTeammateEventHub()
	ch, cancel := h.Subscribe("researcher")

	h.Publish("researcher", agentcore.Event{Type: agentcore.EventAgentStart})
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
	h.Publish("researcher", agentcore.Event{Type: agentcore.EventAgentEnd})
}

// Slow consumers must not stall the publisher. We fill the buffer, publish
// many more events, and assert Publish returns quickly each time.
func TestEventHub_NonBlockingOnSlowConsumer(t *testing.T) {
	h := NewTeammateEventHub()
	_, cancel := h.Subscribe("researcher") // never read from it
	defer cancel()

	start := time.Now()
	for range subBufferSize * 4 {
		h.Publish("researcher", agentcore.Event{Type: agentcore.EventAgentStart})
	}
	elapsed := time.Since(start)
	// Concrete budget: 4×buffer publishes against a deadlocked consumer
	// should be well under 100ms — anything close to a second means we are
	// blocking.
	if elapsed > 100*time.Millisecond {
		t.Errorf("publishing took %v with slow consumer; expected non-blocking", elapsed)
	}
}

func TestEventHub_PresenceBroadcastsFirstPublish(t *testing.T) {
	h := NewTeammateEventHub()
	pch, cancel := h.SubscribePresence()
	defer cancel()

	h.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})
	h.Publish("alice", agentcore.Event{Type: agentcore.EventAgentEnd}) // second publish, no presence
	h.Publish("bob", agentcore.Event{Type: agentcore.EventAgentStart})

	got := drainPresence(t, pch, 2, time.Second)
	if len(got) != 2 {
		t.Fatalf("got %d presence events, want 2: %+v", len(got), got)
	}
	wantNames := map[string]bool{"alice": true, "bob": true}
	for _, ev := range got {
		if !ev.Started {
			t.Errorf("expected Started=true, got %+v", ev)
		}
		if !wantNames[ev.AgentName] {
			t.Errorf("unexpected agent %q in presence events", ev.AgentName)
		}
	}
}

func TestEventHub_PresenceReplaysCurrentRoster(t *testing.T) {
	h := NewTeammateEventHub()
	h.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})
	h.Publish("bob", agentcore.Event{Type: agentcore.EventAgentStart})

	// Late subscriber should still see alice + bob as Started.
	pch, cancel := h.SubscribePresence()
	defer cancel()

	got := drainPresence(t, pch, 2, time.Second)
	if len(got) != 2 {
		t.Fatalf("late subscriber got %d events, want 2: %+v", len(got), got)
	}
}

func TestEventHub_MarkStoppedBroadcasts(t *testing.T) {
	h := NewTeammateEventHub()
	pch, cancel := h.SubscribePresence()
	defer cancel()

	h.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})
	h.MarkStopped("alice")

	got := drainPresence(t, pch, 2, time.Second)
	if len(got) != 2 {
		t.Fatalf("got %d presence events, want 2: %+v", len(got), got)
	}
	if !got[0].Started || got[1].Started {
		t.Errorf("expected [Started, Stopped], got %+v", got)
	}

	// MarkStopped is idempotent.
	h.MarkStopped("alice")
	select {
	case ev := <-pch:
		t.Errorf("duplicate MarkStopped emitted %+v; should be no-op", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// Concurrent publishers + subscribers must not race or deadlock. Race
// detector (`go test -race`) catches the rest; this test just exercises the
// scheduling.
func TestEventHub_Concurrent(t *testing.T) {
	h := NewTeammateEventHub()
	var wg sync.WaitGroup

	// 4 subscribers consuming in tight loops.
	for range 4 {
		ch, cancel := h.Subscribe("a")
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
				h.Publish("a", agentcore.Event{Type: agentcore.EventAgentStart})
			}
		})
	}
	wg.Wait()
}

func TestEventHub_ActiveAgentsReflectsState(t *testing.T) {
	h := NewTeammateEventHub()
	if got := h.ActiveAgents(); len(got) != 0 {
		t.Errorf("initial ActiveAgents = %v, want empty", got)
	}
	h.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})
	h.Publish("bob", agentcore.Event{Type: agentcore.EventAgentStart})
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

func drainPresence(t *testing.T, ch <-chan PresenceEvent, n int, timeout time.Duration) []PresenceEvent {
	t.Helper()
	out := make([]PresenceEvent, 0, n)
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
