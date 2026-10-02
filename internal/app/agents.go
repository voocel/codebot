package app

import (
	"sync"

	"github.com/voocel/agentcore"
)

// AgentHub is a fan-out point for events produced by background sub-agent
// runs, which reach it through their Config.Emit; whatever observes their
// activity (the TUI transcript view) subscribes here.
//
// Design constraints:
//   - Publish is hot-path (called once per event by every run goroutine).
//     It MUST NOT block on a slow subscriber, or it stalls the run that
//     produced the event. Each subscriber gets a buffered chan with a
//     drop-oldest policy.
//   - Subscribers come and go (modal opens/closes). Subscribe returns an
//     unsubscribe function instead of exposing the underlying map.
//   - Late subscribers must see what they missed. Every published event but
//     the streamed deltas, which the MessageEnd that follows them repeats, is
//     also appended to a per-agent ring buffer; Subscribe hands back a
//     snapshot before wiring the live channel. The ring outlives MarkStopped
//     so an observer can open an agent's transcript after it has finished.
type AgentHub struct {
	mu     sync.RWMutex
	subs   map[string]map[int]chan agentcore.Event // agentName → subId → chan
	nextID int

	// active tracks agents currently publishing: set by Publish, cleared by
	// MarkStopped. The history ring is NOT cleared on stop — late observers
	// can still review a finished agent's transcript.
	active map[string]bool

	// history retains a bounded replay buffer per agent. Survives MarkStopped
	// until the hub itself is discarded (session end). nil ring == agent has
	// never published.
	history map[string]*eventRing
}

// AgentInfo describes a known agent: its name and whether it is still
// publishing events. Returned by KnownAgents so the UI can render an "ended"
// indicator without a second round-trip.
type AgentInfo struct {
	Name   string
	Active bool
}

// subBufferSize bounds a subscriber's live queue. Large enough that a normal
// UI catches up trivially; small enough that an unresponsive subscriber's
// memory growth is capped. With drop-oldest we never block, so this is a
// memory cap rather than a correctness knob.
const subBufferSize = 64

// historyCapacity bounds the per-agent replay ring. A typical turn
// produces ~5–10 events (message start and end, tool start and end, …); 512
// holds roughly the last 50–100 turns. Above that the oldest events scroll
// off — a user opening the modal sees a truncated head but the tail is
// always current.
const historyCapacity = 512

// NewAgentHub returns an empty hub ready to use.
func NewAgentHub() *AgentHub {
	return &AgentHub{
		subs:    make(map[string]map[int]chan agentcore.Event),
		active:  make(map[string]bool),
		history: make(map[string]*eventRing),
	}
}

// Publish delivers ev to every current subscriber of agentName and appends it
// to the per-agent history ring. Non-blocking: if a subscriber's buffer is
// full, the oldest queued event is dropped to make room — slow consumers lose
// history, never block the publisher. It marks agentName active.
//
// Lock discipline: Publish does ring write + chan sends inside the mutex, in
// strict serialisation with Subscribe's unsubscribe (which closes the chan).
// Without that ordering, a subscriber that cancels mid-publish would let us
// send on a closed chan and panic. The sends themselves are non-blocking
// (drop-oldest), so holding the lock briefly is fine — the hot path is
// O(subscribers) channel operations, not I/O.
func (h *AgentHub) Publish(agentName string, ev agentcore.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	ring, exists := h.history[agentName]
	if !exists {
		ring = newEventRing(historyCapacity)
		h.history[agentName] = ring
	}
	if _, delta := ev.(agentcore.MessageDelta); !delta {
		ring.push(ev)
	}
	h.active[agentName] = true
	for _, ch := range h.subs[agentName] {
		nonBlockingSend(ch, ev)
	}
}

// MarkStopped marks agentName as no longer publishing. Its history stays, so
// an observer can still open its transcript.
func (h *AgentHub) MarkStopped(agentName string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active[agentName] = false
}

// Subscribe registers a listener for agentName's events. Returns the recorded
// history as a snapshot slice (oldest first) plus a live channel for events
// arriving after the snapshot was taken. The caller MUST consume the history
// slice before reading the channel so its transcript renders in order.
//
// The channel is buffered (subBufferSize); the publisher drops the oldest
// queued event when full. cancel MUST be called when the listener is done —
// it removes the channel from the routing table and closes it.
func (h *AgentHub) Subscribe(agentName string) ([]agentcore.Event, <-chan agentcore.Event, func()) {
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

// ActiveAgents returns the names that are currently publishing — i.e. have
// published at least once and have not been MarkStopped'd. For the broader
// roster (including agents that already finished) use KnownAgents.
func (h *AgentHub) ActiveAgents() []string {
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

// KnownAgents returns every agent that has ever published an event in this
// session, alongside its current active flag. Use this for "which agents
// can I open in the transcript modal?" — already-finished agents still have
// a readable history.
func (h *AgentHub) KnownAgents() []AgentInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]AgentInfo, 0, len(h.history))
	for name := range h.history {
		out = append(out, AgentInfo{Name: name, Active: h.active[name]})
	}
	return out
}

// IsActive reports whether agentName is currently publishing events. Returns
// false for unknown names and for known-but-stopped agents.
func (h *AgentHub) IsActive(agentName string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.active[agentName]
}

// eventRing is a fixed-capacity circular buffer of events. push is O(1) and
// overwrites the oldest slot when full; snapshot returns a freshly-allocated
// slice in chronological order. Not safe for concurrent use — callers hold
// AgentHub.mu while touching it.
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
// silent — the UI sees a discontinuity in its event stream but the run
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
