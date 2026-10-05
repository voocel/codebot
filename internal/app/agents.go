package app

import (
	"sync"

	"github.com/voocel/agentcore"
)

// AgentHub fans out the events of background sub-agent runs, which reach it
// through their Config.Emit, to whatever observes them (the TUI transcript
// view). Publishing never blocks a run: a subscriber that falls behind loses
// its oldest events. Each agent's events but the streamed deltas, which the
// MessageEnd after them repeats, are kept in a bounded ring, so a late
// subscriber, even one opening a finished agent, sees what it missed.
type AgentHub struct {
	mu      sync.RWMutex
	subs    map[string]map[int]chan agentcore.Event // agent → subscription → channel
	nextID  int
	active  map[string]bool       // agents publishing: set by Publish, cleared by MarkStopped
	history map[string]*eventRing // agents that have published, stopped ones included
}

// AgentInfo is an agent the hub knows and whether it is still publishing.
type AgentInfo struct {
	Name   string
	Active bool
}

// subBufferSize bounds a subscriber's live queue.
const subBufferSize = 64

// historyCapacity bounds an agent's replay ring: some 50–100 turns of 5–10
// events each.
const historyCapacity = 512

// NewAgentHub returns an empty hub ready to use.
func NewAgentHub() *AgentHub {
	return &AgentHub{
		subs:    make(map[string]map[int]chan agentcore.Event),
		active:  make(map[string]bool),
		history: make(map[string]*eventRing),
	}
}

// Publish records ev in agentName's history, marks the agent active and
// sends ev to its subscribers. It sends under the lock, so an unsubscribe,
// which closes the channel, cannot race it; the sends do not block.
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

// Subscribe returns agentName's history, oldest first, and a channel of the
// events published after it; handle the history first. cancel ends the
// subscription and closes the channel.
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

// ActiveAgents returns the agents publishing.
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

// KnownAgents returns every agent that has published, finished ones
// included, whose history can still be read.
func (h *AgentHub) KnownAgents() []AgentInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]AgentInfo, 0, len(h.history))
	for name := range h.history {
		out = append(out, AgentInfo{Name: name, Active: h.active[name]})
	}
	return out
}

// IsActive reports whether agentName is publishing.
func (h *AgentHub) IsActive(agentName string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.active[agentName]
}

// eventRing keeps the latest events, overwriting the oldest when full.
// Callers hold AgentHub.mu.
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
