package agent

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/storage"
)

// persistState owns the session-log store reference and the lazy-persist
// bookkeeping. The RWMutex guards the store IDENTITY, not its I/O (Store has
// its own lock): appenders run under RLock so a concurrent swapStore (Reset /
// SwitchSession) cannot close the store out from under an in-flight append —
// swapStore's write lock waits for them to drain. Read-only identity lookups
// (Header / BuildSnapshot) use currentStore instead of holding the RLock
// across long I/O; storage.Store self-protects against use-after-Close.
type persistState struct {
	mu                 sync.RWMutex
	store              *storage.Store
	pendingUserMsg     []agentcore.Message
	autoNamed          bool
	lastAssistantStart time.Time // set at EventMessageStart (assistant), consumed at EventMessageEnd for latency_ms

	// flushMu serializes flushPending drains end-to-end (incl. I/O) — see
	// that method for why overlapping drains corrupt the queue.
	flushMu sync.Mutex
}

func (p *persistState) currentStore() *storage.Store {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.store
}

// withStore runs fn against the bound store under RLock, so Reset /
// SwitchSession cannot close it mid-append. No-op when no store is bound.
func (p *persistState) withStore(fn func(*storage.Store) error) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.store == nil {
		return nil
	}
	return fn(p.store)
}

// swapStore installs the new session log and returns the old one for the
// caller to close. Pending lazy-persist messages belong to the conversation
// being torn down and are dropped — flushing them into the NEW store would
// cross conversations. flushMu is taken first (order: flushMu → p.mu) so an
// in-flight flushPending drain finishes before the identity changes: the
// drain re-resolves the store between reading a message and popping it, and
// a swap landing in that gap would append an old-session message to the new
// store, then pop a message the new session had just queued.
func (p *persistState) swapStore(newStore *storage.Store, autoNamed bool) *storage.Store {
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.store
	p.store = newStore
	p.autoNamed = autoNamed
	p.pendingUserMsg = nil
	p.lastAssistantStart = time.Time{}
	return old
}

// claimAutoName atomically claims the one-shot auto-naming slot; returns the
// store to name, or nil when already claimed / no store.
func (p *persistState) claimAutoName() *storage.Store {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.autoNamed || p.store == nil {
		return nil
	}
	p.autoNamed = true
	return p.store
}

func (p *persistState) queuePending(msg agentcore.Message) {
	p.mu.Lock()
	p.pendingUserMsg = append(p.pendingUserMsg, msg)
	p.mu.Unlock()
}

func (p *persistState) markAssistantStart() {
	p.mu.Lock()
	p.lastAssistantStart = time.Now()
	p.mu.Unlock()
}

func (p *persistState) takeAssistantStart() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	start := p.lastAssistantStart
	p.lastAssistantStart = time.Time{}
	return start
}

// append persists one message, annotating storage errors with any invalid
// tool-call args (the usual root cause when an append is rejected).
func (p *persistState) append(msg agentcore.Message) error {
	return p.withStore(func(store *storage.Store) error {
		if err := store.AppendMessage(msg); err != nil {
			detail := err.Error()
			for _, tc := range msg.ToolCalls() {
				if !json.Valid(tc.Args) {
					detail = fmt.Sprintf("%s [invalid args in %s(%s): %s]",
						detail, tc.Name, tc.ID, truncateBytes(tc.Args, 200))
				}
			}
			return fmt.Errorf("persist message: %s", detail)
		}
		return nil
	})
}

// flushPending drains the lazy-persist queue. flushMu serializes whole
// drains: the committer path (agent loop goroutine), EventAgentEnd (event
// goroutine), and Close (UI) can all arrive concurrently, and two interleaved
// drains would each persist pendingUserMsg[0] then each pop — duplicating one
// message and dropping another. A second flusher blocks until the first
// finishes, then sees an empty queue. Lock order is flushMu → p.mu.
func (p *persistState) flushPending() error {
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	for {
		p.mu.Lock()
		if len(p.pendingUserMsg) == 0 {
			p.mu.Unlock()
			return nil
		}
		msg := p.pendingUserMsg[0]
		p.mu.Unlock()

		if err := p.append(msg); err != nil {
			return err
		}
		p.mu.Lock()
		if len(p.pendingUserMsg) > 0 {
			p.pendingUserMsg = p.pendingUserMsg[1:]
		}
		p.mu.Unlock()
	}
}
