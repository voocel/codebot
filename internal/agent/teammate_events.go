package agent

import (
	"sync"

	"github.com/voocel/agentcore"
)

// TeammateEventHub is a fan-out point for events produced by teammate agent
// loops. agentcore.AgentLoop returns a single-consumer channel — the executor
// drains it for produced-message collection — so anything else that wants to
// observe a teammate's activity (UI transcript view, log sinks, future
// analytics) must subscribe here.
//
// Design constraints:
//   - Publish is hot-path (called once per event by every teammate goroutine).
//     It MUST NOT block on a slow subscriber, or it stalls the AgentLoop that
//     produced the event. Each subscriber gets a buffered chan with a
//     drop-oldest policy.
//   - Subscribers come and go (modal opens/closes). Subscribe returns an
//     unsubscribe function instead of exposing the underlying map.
//   - Presence (a teammate started / stopped publishing) is its own broadcast
//     so a UI can auto-focus on the first teammate to come online without
//     polling task.Runtime.
//
// Zero-value safety: a nil *TeammateEventHub is a valid no-op publisher —
// callers (TeammateSpawner) can be wired before the hub exists in tests.
type TeammateEventHub struct {
	mu       sync.RWMutex
	subs     map[string]map[int]chan agentcore.Event // agentName → subId → chan
	presSubs map[int]chan PresenceEvent
	nextID   int

	// Active tracks teammates that have published at least one event. Used
	// to suppress duplicate "started" presence broadcasts and to enable
	// late subscribers to learn the current roster via ActiveAgents().
	active map[string]bool
}

// PresenceEvent describes a teammate joining or leaving the hub. Started is
// emitted on the first Publish for an agent; Stopped is emitted by an explicit
// MarkStopped call (the spawner invokes this when the teammate's goroutine
// exits, so subscribers can release resources without polling).
type PresenceEvent struct {
	AgentName string
	Started   bool // true = joined, false = left
}

// subBufferSize bounds a subscriber's queue. Large enough that a normal UI
// catches up trivially; small enough that an unresponsive subscriber's memory
// growth is capped. With drop-oldest we never block, so this is a memory cap
// rather than a correctness knob.
const subBufferSize = 64

// NewTeammateEventHub returns an empty hub ready to use.
func NewTeammateEventHub() *TeammateEventHub {
	return &TeammateEventHub{
		subs:     make(map[string]map[int]chan agentcore.Event),
		presSubs: make(map[int]chan PresenceEvent),
		active:   make(map[string]bool),
	}
}

// Publish delivers ev to every current subscriber of agentName. Non-blocking:
// if a subscriber's buffer is full, the oldest queued event is dropped to make
// room — slow consumers lose history, never block the publisher.
//
// The first Publish for a given agentName also broadcasts a PresenceEvent
// {Started: true} so UI subscribers can self-attach without separate plumbing.
//
// nil receiver is a no-op so spawner wiring stays simple in tests.
//
// Lock discipline: Publish does the actual chan sends inside the mutex, in
// strict serialisation with Subscribe's unsubscribe (which closes the chan).
// Without that ordering, a subscriber that cancels mid-publish would let us
// send on a closed chan and panic. The sends themselves are non-blocking
// (drop-oldest), so holding the lock briefly is fine — the hot path is
// O(subscribers) channel operations, not I/O.
func (h *TeammateEventHub) Publish(agentName string, ev agentcore.Event) {
	if h == nil || agentName == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	firstPublish := !h.active[agentName]
	if firstPublish {
		h.active[agentName] = true
	}
	for _, ch := range h.subs[agentName] {
		nonBlockingSend(ch, ev)
	}
	if firstPublish {
		pe := PresenceEvent{AgentName: agentName, Started: true}
		for _, ch := range h.presSubs {
			nonBlockingSendPresence(ch, pe)
		}
	}
}

// MarkStopped emits a Stopped presence event and clears the active flag so a
// future Publish would re-emit Started. Safe to call multiple times — only
// the first call after a Started transition broadcasts.
func (h *TeammateEventHub) MarkStopped(agentName string) {
	if h == nil || agentName == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active[agentName] {
		return
	}
	delete(h.active, agentName)
	pe := PresenceEvent{AgentName: agentName, Started: false}
	for _, ch := range h.presSubs {
		nonBlockingSendPresence(ch, pe)
	}
}

// Subscribe registers a listener for agentName's events. The returned channel
// is buffered (subBufferSize); the publisher drops the oldest event when full.
// Unsubscribe MUST be called when the listener is done — it removes the
// channel from the routing table and closes it.
func (h *TeammateEventHub) Subscribe(agentName string) (<-chan agentcore.Event, func()) {
	if h == nil {
		ch := make(chan agentcore.Event)
		close(ch)
		return ch, func() {}
	}
	ch := make(chan agentcore.Event, subBufferSize)
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	if h.subs[agentName] == nil {
		h.subs[agentName] = make(map[int]chan agentcore.Event)
	}
	h.subs[agentName][id] = ch
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		if subs, ok := h.subs[agentName]; ok {
			if existing, found := subs[id]; found && existing == ch {
				delete(subs, id)
				if len(subs) == 0 {
					delete(h.subs, agentName)
				}
				close(ch)
			}
		}
		h.mu.Unlock()
	}
}

// SubscribePresence returns a channel that receives a PresenceEvent every
// time a teammate first publishes (Started) or is marked stopped. Channel is
// buffered; unsubscribe closes it.
//
// The current active roster is replayed as Started events on the returned
// channel before any future presence changes — late subscribers don't miss
// teammates that joined earlier.
func (h *TeammateEventHub) SubscribePresence() (<-chan PresenceEvent, func()) {
	if h == nil {
		ch := make(chan PresenceEvent)
		close(ch)
		return ch, func() {}
	}
	ch := make(chan PresenceEvent, subBufferSize)
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	h.presSubs[id] = ch
	// Replay current roster while still holding the lock so no Publish can
	// race in between and double-fire.
	for name := range h.active {
		nonBlockingSendPresence(ch, PresenceEvent{AgentName: name, Started: true})
	}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		if existing, ok := h.presSubs[id]; ok && existing == ch {
			delete(h.presSubs, id)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// ActiveAgents returns the names that have published at least one event and
// have not yet been MarkStopped'd. Useful for the UI's "which teammate to
// focus on" decision when opening a modal.
func (h *TeammateEventHub) ActiveAgents() []string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.active))
	for name := range h.active {
		out = append(out, name)
	}
	return out
}

// nonBlockingSend pushes ev onto ch; if ch is full, drops the oldest queued
// event and retries. This guarantees the publisher never blocks. The drop is
// silent — the UI sees a discontinuity in its event stream but the teammate
// goroutine stays responsive.
func nonBlockingSend(ch chan agentcore.Event, ev agentcore.Event) {
	for {
		select {
		case ch <- ev:
			return
		default:
			// Drain one event to make room. If another goroutine raced us
			// and emptied the channel, the next iteration's send succeeds.
			select {
			case <-ch:
			default:
				// Channel emptied between the two selects — retry send.
			}
		}
	}
}

// nonBlockingSendPresence is the same dance for presence events. Kept as a
// separate function because Go generics on channels would require either
// any-typed channels (loses static safety) or a generic wrapper struct that
// just adds boilerplate.
func nonBlockingSendPresence(ch chan PresenceEvent, ev PresenceEvent) {
	for {
		select {
		case ch <- ev:
			return
		default:
			select {
			case <-ch:
			default:
			}
		}
	}
}
