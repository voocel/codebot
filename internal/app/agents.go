package app

import (
	"sync"

	"github.com/voocel/agentcore"
)

// AgentHub fans out events from background sub-agent runs to observers such
// as the TUI. Publishing never blocks a run: a slow subscriber loses its
// oldest events. Each agent's events, except streamed deltas (the following
// MessageEnd repeats them), are kept in a bounded ring, so a late subscriber
// still sees the history, even of a finished agent.
type AgentHub struct {
	mu      sync.RWMutex
	subs    map[string]map[int]chan agentcore.Event // agent → subscription → channel
	nextID  int
	active  map[string]bool
	history map[string]*eventRing
}

const subBufferSize = 64

// historyCapacity is roughly 50–100 turns of 5–10 events each.
const historyCapacity = 512

func NewAgentHub() *AgentHub {
	return &AgentHub{
		subs:    make(map[string]map[int]chan agentcore.Event),
		active:  make(map[string]bool),
		history: make(map[string]*eventRing),
	}
}

// Publish sends under the lock so it can't race an unsubscribe closing the
// channel. The sends never block.
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

func (h *AgentHub) MarkStopped(agentName string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active[agentName] = false
}

// Subscribe returns the agent's history, oldest first, and a channel of later
// events; handle the history first. cancel closes the channel.
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

func (h *AgentHub) IsActive(agentName string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.active[agentName]
}

// eventRing overwrites the oldest events when full. Callers hold
// AgentHub.mu.
type eventRing struct {
	buf  []agentcore.Event
	head int  // next write position
	full bool // buf has wrapped at least once
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

// nonBlockingSend sends ev on ch, dropping the oldest queued event when ch
// is full.
func nonBlockingSend(ch chan agentcore.Event, ev agentcore.Event) {
	for {
		select {
		case ch <- ev:
			return
		default:
			select {
			case <-ch:
			default: // the subscriber drained it meanwhile
			}
		}
	}
}
