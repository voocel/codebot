package app

import (
	"sync"

	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/session"
)

// Kind identifies an App event.
type Kind int

const (
	// SessionEvent carries an event of the current conversation in Session.
	SessionEvent Kind = iota
	// Opened says Conversation became the current conversation.
	Opened
	// ModeChanged says the permission mode changed to Mode.
	ModeChanged
	// MCPChanged says the MCP tools or instructions changed.
	MCPChanged
)

// Event is an App event.
type Event struct {
	Kind         Kind
	Session      session.Event
	Conversation *Conversation
	Mode         interact.Mode
}

// broadcaster calls every subscriber with each event, on the publishing
// goroutine.
type broadcaster struct {
	mu   sync.Mutex
	next int
	subs map[int]func(Event)
}

func (b *broadcaster) subscribe(fn func(Event)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs == nil {
		b.subs = make(map[int]func(Event))
	}
	id := b.next
	b.next++
	b.subs[id] = fn
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs, id)
	}
}

func (b *broadcaster) publish(ev Event) {
	b.mu.Lock()
	subs := make([]func(Event), 0, len(b.subs))
	for _, fn := range b.subs {
		subs = append(subs, fn)
	}
	b.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}
