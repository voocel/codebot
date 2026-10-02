package session

import (
	"sync"
	"sync/atomic"

	"github.com/voocel/agentcore"
)

// Kind identifies a session event.
type Kind int

const (
	// Agent passes a loop event through unchanged. A manual compaction is
	// reported with the loop's compaction events as well.
	Agent Kind = iota
	// RunStarted precedes the events of a run.
	RunStarted
	// Idle follows the last event of a run (or manual compaction) when
	// nothing else is left to run.
	Idle
	// StatusChanged says Status has changed.
	StatusChanged
	// Error reports a failure outside any run, such as recording a model
	// change.
	Error
)

// Event is a session event.
type Event struct {
	Kind  Kind
	Agent agentcore.Event // Kind == Agent
	Err   error           // Kind == Error
}

// hub fans events out to subscribers, each with an unbounded queue drained
// by its own goroutine, so publishing never blocks.
type hub struct {
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	closed bool
}

type subscriber struct {
	fn      func(Event)
	stopped atomic.Bool

	mu        sync.Mutex
	cond      *sync.Cond // signals queued events, delivery progress and exit
	queue     []Event
	queued    uint64 // events ever queued
	delivered uint64 // events handed to fn
	done      bool   // no more events will be queued
	exited    bool   // deliver has returned
}

func (h *hub) subscribe(fn func(Event)) func() {
	sub := &subscriber{fn: fn}
	sub.cond = sync.NewCond(&sub.mu)
	h.mu.Lock()
	if h.closed {
		sub.done = true
	} else {
		if h.subs == nil {
			h.subs = make(map[*subscriber]struct{})
		}
		h.subs[sub] = struct{}{}
	}
	h.mu.Unlock()
	go sub.deliver()

	return func() {
		h.mu.Lock()
		delete(h.subs, sub)
		h.mu.Unlock()
		sub.stopped.Store(true)
		sub.end()
	}
}

func (h *hub) publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		sub.mu.Lock()
		sub.queue = append(sub.queue, ev)
		sub.queued++
		sub.mu.Unlock()
		sub.cond.Broadcast()
	}
}

// mark returns a function that waits until every current subscriber has
// been handed the events published so far.
func (h *hub) mark() func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	var waits []func()
	for sub := range h.subs {
		sub.mu.Lock()
		n := sub.queued
		sub.mu.Unlock()
		waits = append(waits, func() {
			sub.mu.Lock()
			defer sub.mu.Unlock()
			for sub.delivered < n && !sub.exited {
				sub.cond.Wait()
			}
		})
	}
	return func() {
		for _, wait := range waits {
			wait()
		}
	}
}

// close ends every subscription once its queue is drained.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for sub := range h.subs {
		sub.end()
	}
	h.subs = nil
}

func (sub *subscriber) end() {
	sub.mu.Lock()
	sub.done = true
	sub.mu.Unlock()
	sub.cond.Broadcast()
}

func (sub *subscriber) deliver() {
	defer func() {
		sub.mu.Lock()
		sub.exited = true
		sub.mu.Unlock()
		sub.cond.Broadcast()
	}()
	for {
		sub.mu.Lock()
		for len(sub.queue) == 0 && !sub.done {
			sub.cond.Wait()
		}
		batch := sub.queue
		sub.queue = nil
		sub.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		for _, ev := range batch {
			if sub.stopped.Load() {
				return
			}
			sub.fn(ev)
			sub.mu.Lock()
			sub.delivered++
			sub.mu.Unlock()
			sub.cond.Broadcast()
		}
	}
}
