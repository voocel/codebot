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
//   - Late subscribers must see what they missed. Every published event is
//     also appended to a per-agent ring buffer; Subscribe hands back a
//     snapshot before wiring the live channel. The ring outlives MarkStopped
//     so an Observer can open a teammate's transcript after it has finished.
//
// Zero-value safety: a nil *TeammateEventHub is a valid no-op publisher —
// callers (TeammateSpawner) can be wired before the hub exists in tests.
type TeammateEventHub struct {
	mu       sync.RWMutex
	subs     map[string]map[int]chan agentcore.Event // agentName → subId → chan
	presSubs map[int]chan PresenceEvent
	nextID   int

	// active tracks teammates currently publishing. Flipped on first Publish
	// (broadcasts Started) and back by MarkStopped (broadcasts Stopped). The
	// associated history ring is NOT cleared on stop — late observers can
	// still review a finished teammate's transcript.
	active map[string]bool

	// history retains a bounded replay buffer per agent. Survives MarkStopped
	// until the hub itself is discarded (session end). nil ring == agent has
	// never published.
	history map[string]*eventRing
}

// AgentInfo describes a known teammate: its name and whether it is still
// publishing events. Returned by KnownAgents so the UI can render an "ended"
// indicator without a second round-trip.
type AgentInfo struct {
	Name   string
	Active bool
}

// PresenceEvent describes a teammate joining or leaving the hub. Started is
// emitted on the first Publish for an agent; Stopped is emitted by an explicit
// MarkStopped call (the spawner invokes this when the teammate's goroutine
// exits, so subscribers can release resources without polling).
type PresenceEvent struct {
	AgentName string
	Started   bool // true = joined, false = left
}

// subBufferSize bounds a subscriber's live queue. Large enough that a normal
// UI catches up trivially; small enough that an unresponsive subscriber's
// memory growth is capped. With drop-oldest we never block, so this is a
// memory cap rather than a correctness knob.
const subBufferSize = 64

// historyCapacity bounds the per-agent replay ring. A typical teammate turn
// produces ~5–10 events (start, message ends, tool exec start/finish, …); 512
// holds roughly the last 50–100 turns. Above that the oldest events scroll
// off — a user opening the modal sees a truncated head but the tail is
// always current.
const historyCapacity = 512

// NewTeammateEventHub returns an empty hub ready to use.
func NewTeammateEventHub() *TeammateEventHub {
	return &TeammateEventHub{
		subs:     make(map[string]map[int]chan agentcore.Event),
		presSubs: make(map[int]chan PresenceEvent),
		active:   make(map[string]bool),
		history:  make(map[string]*eventRing),
	}
}

// Publish delivers ev to every current subscriber of agentName and appends it
// to the per-agent history ring. Non-blocking: if a subscriber's buffer is
// full, the oldest queued event is dropped to make room — slow consumers lose
// history, never block the publisher.
//
// The first Publish for a (currently-stopped) agentName also broadcasts a
// PresenceEvent {Started: true}. "Stopped → publishes again" repeats the
// broadcast; this is intentional so a UI auto-attaching to active teammates
// catches a teammate that briefly went idle and resumed.
//
// nil receiver is a no-op so spawner wiring stays simple in tests.
//
// Lock discipline: Publish does ring write + chan sends inside the mutex, in
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

	ring, exists := h.history[agentName]
	if !exists {
		ring = newEventRing(historyCapacity)
		h.history[agentName] = ring
	}
	ring.push(ev)

	wasActive := h.active[agentName]
	if !wasActive {
		h.active[agentName] = true
	}
	for _, ch := range h.subs[agentName] {
		nonBlockingSend(ch, ev)
	}
	if !wasActive {
		pe := PresenceEvent{AgentName: agentName, Started: true}
		for _, ch := range h.presSubs {
			nonBlockingSendPresence(ch, pe)
		}
	}
}

// MarkStopped emits a Stopped presence event and flips the active flag. The
// history ring is preserved so an observer can still open this teammate's
// transcript later. Safe to call multiple times — only the first call after
// a Started transition broadcasts.
func (h *TeammateEventHub) MarkStopped(agentName string) {
	if h == nil || agentName == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active[agentName] {
		return
	}
	h.active[agentName] = false
	pe := PresenceEvent{AgentName: agentName, Started: false}
	for _, ch := range h.presSubs {
		nonBlockingSendPresence(ch, pe)
	}
}

// Subscribe registers a listener for agentName's events. Returns the recorded
// history as a snapshot slice (oldest first) plus a live channel for events
// arriving after the snapshot was taken. The caller MUST consume the history
// slice before reading the channel so its transcript renders in order.
//
// The channel is buffered (subBufferSize); the publisher drops the oldest
// queued event when full. cancel MUST be called when the listener is done —
// it removes the channel from the routing table and closes it.
func (h *TeammateEventHub) Subscribe(agentName string) ([]agentcore.Event, <-chan agentcore.Event, func()) {
	if h == nil {
		ch := make(chan agentcore.Event)
		close(ch)
		return nil, ch, func() {}
	}
	ch := make(chan agentcore.Event, subBufferSize)
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	if h.subs[agentName] == nil {
		h.subs[agentName] = make(map[int]chan agentcore.Event)
	}
	h.subs[agentName][id] = ch

	var history []agentcore.Event
	if ring, ok := h.history[agentName]; ok {
		history = ring.snapshot()
	}
	h.mu.Unlock()

	return history, ch, func() {
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
	for name, isActive := range h.active {
		if !isActive {
			continue
		}
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

// ActiveAgents returns the names that are currently publishing — i.e. have
// published at least once and have not been MarkStopped'd. For the broader
// roster (including teammates that already finished) use KnownAgents.
func (h *TeammateEventHub) ActiveAgents() []string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.active))
	for name, isActive := range h.active {
		if isActive {
			out = append(out, name)
		}
	}
	return out
}

// KnownAgents returns every teammate that has ever published an event in this
// session, alongside its current active flag. Use this for "which teammates
// can I open in the transcript modal?" — already-finished agents still have
// a readable history.
func (h *TeammateEventHub) KnownAgents() []AgentInfo {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]AgentInfo, 0, len(h.history))
	for name := range h.history {
		out = append(out, AgentInfo{Name: name, Active: h.active[name]})
	}
	return out
}

// IsActive reports whether agentName is currently publishing events. Returns
// false for unknown names and for known-but-stopped teammates.
func (h *TeammateEventHub) IsActive(agentName string) bool {
	if h == nil || agentName == "" {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.active[agentName]
}

// eventRing is a fixed-capacity circular buffer of events. push is O(1) and
// overwrites the oldest slot when full; snapshot returns a freshly-allocated
// slice in chronological order. Not safe for concurrent use — callers hold
// TeammateEventHub.mu while touching it.
type eventRing struct {
	buf  []agentcore.Event
	head int  // next write position
	full bool // true once buf has wrapped at least once
}

func newEventRing(capacity int) *eventRing {
	return &eventRing{buf: make([]agentcore.Event, capacity)}
}

func (r *eventRing) push(ev agentcore.Event) {
	r.buf[r.head] = ev
	r.head = (r.head + 1) % len(r.buf)
	if r.head == 0 {
		r.full = true
	}
}

func (r *eventRing) snapshot() []agentcore.Event {
	if !r.full {
		out := make([]agentcore.Event, r.head)
		copy(out, r.buf[:r.head])
		return out
	}
	out := make([]agentcore.Event, len(r.buf))
	n := copy(out, r.buf[r.head:])
	copy(out[n:], r.buf[:r.head])
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
