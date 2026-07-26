package tui

import tea "github.com/charmbracelet/bubbletea"

// dialogCard is a modal "waiting on user" surface: permission consent, plan
// approval, or ask_user questions. All of them block a tool-gate goroutine on
// a response channel; the queue below owns their shared lifecycle so parallel
// tool calls can raise several without racing over the screen.
type dialogCard interface {
	// key identifies the card for targeted dismissal — by convention the
	// response channel the gate side is blocked on.
	key() any
	// handleKey processes a key press while the card is front-most. Cards
	// mark themselves finished (answered or dismissed) as a side effect.
	handleKey(m *Model, msg tea.KeyMsg) (handled bool, cmd tea.Cmd)
	// finished reports whether the card has delivered its outcome.
	finished() bool
	// abort force-closes a pending card with its no-answer outcome so the
	// blocked gate goroutine is released. Must be idempotent.
	abort()
	// render draws the card at the current terminal size.
	render(m *Model) string
	// hidesContextBar reports whether the card owns the whole bottom region
	// (ask_user) or coexists with the context bar (permission cards).
	hidesContextBar() bool
}

// dialogQueue serializes dialog cards: cards[0] is on screen, the rest wait
// in arrival order. Zero value is ready to use.
type dialogQueue struct {
	cards []dialogCard
}

func (q *dialogQueue) push(c dialogCard) { q.cards = append(q.cards, c) }

// active returns the on-screen card, or nil when no dialog is open.
func (q *dialogQueue) active() dialogCard {
	if len(q.cards) == 0 {
		return nil
	}
	return q.cards[0]
}

// prune drops finished cards from the front so the next queued card surfaces.
func (q *dialogQueue) prune() {
	for len(q.cards) > 0 && q.cards[0].finished() {
		q.cards = q.cards[1:]
	}
}

// dismiss aborts and removes the card identified by key. The gate side sends
// a dismiss message when its context is cancelled, so a card whose asker
// stopped listening never lingers on screen.
func (q *dialogQueue) dismiss(key any) {
	for i, c := range q.cards {
		if c.key() == key {
			c.abort()
			q.cards = append(q.cards[:i], q.cards[i+1:]...)
			return
		}
	}
}

// abortAll force-closes every card. Turn-teardown safety net: any card still
// visible after the run died has nobody listening for its answer.
func (q *dialogQueue) abortAll() {
	for _, c := range q.cards {
		c.abort()
	}
	q.cards = nil
}
