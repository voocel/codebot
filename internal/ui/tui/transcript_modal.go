package tui

import (
	"fmt"
	"slices"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
)

// Transcript modal — full-screen popup that lets the user observe a
// teammate's live AgentLoop output. Activated with Ctrl+O (toggle) when at
// least one teammate is registered with the event hub; closed with Esc or
// the same Ctrl+O. While open, the modal owns the entire viewport and
// intercepts all keys except shutdown/abort gestures (Ctrl+C) and modal
// navigation (Tab/Shift+Tab, j/k/PgUp/PgDn, g/G).
//
// Wire shape:
//
//   Open  → Subscribe(agentName) → start a tea.Cmd that drains the hub
//           channel one event at a time, posting TranscriptEventMsg back
//           into Update. Each msg handler reschedules the next read so
//           we stay in the Elm cycle.
//   Close → call the unsubscribe closure (closes the chan), reset all
//           modal state. The pending read returns TranscriptChannelClosedMsg
//           which Update drops because the modal is already gone.
//
// Tradeoffs:
//   - No background goroutine: every read is one tea.Cmd. Costs an extra
//     scheduling hop per event vs. a long-running goroutine + p.Send, but
//     avoids the need to plumb tea.Program through Model and makes
//     lifecycle deterministic (a "stuck" cmd is just a leaked goroutine,
//     not a leaked subscription).
//   - The hub already drops oldest on slow consumers, so latency-induced
//     drops are invisible here.

// TranscriptEventMsg is a teammate event delivered to Update for the open
// modal. The agent name is included so a late-arriving event for a teammate
// the user has since switched away from can be discarded.
type TranscriptEventMsg struct {
	Agent string
	Event agentcore.Event
}

// TranscriptChannelClosedMsg signals the hub subscription closed (modal
// closed via Esc, teammate disappeared, etc.). Used purely to break the
// cmd → msg → cmd recursion; Update reacts only if the modal is still open.
type TranscriptChannelClosedMsg struct {
	Agent string
}

// transcriptModalOpenable reports whether the modal can open right now:
// the hub must exist and at least one teammate must have published events
// (live or already finished — finished agents still have a readable history).
func (m *Model) transcriptModalOpenable() bool {
	if m.config.TeammateEvents == nil {
		return false
	}
	return len(m.config.TeammateEvents.KnownAgents()) > 0
}

// knownAgentNames returns the sorted list of all teammates the hub has ever
// seen (active + stopped). Sorting keeps Ctrl+O and Tab/Shift+Tab order
// deterministic across redraws.
func knownAgentNames(hub *agent.TeammateEventHub) []string {
	if hub == nil {
		return nil
	}
	infos := hub.KnownAgents()
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = info.Name
	}
	slices.Sort(names)
	return names
}

// modalTitleFor renders the title shown at the top of the transcript modal.
// Active teammates appear plain; stopped teammates get an "(ended)" suffix
// so the user can tell at a glance whether they're watching live output or
// a frozen replay.
func modalTitleFor(hub *agent.TeammateEventHub, agentName string) string {
	if hub != nil && !hub.IsActive(agentName) {
		return fmt.Sprintf("teammate: %s (ended)", agentName)
	}
	return fmt.Sprintf("teammate: %s", agentName)
}

// openTranscriptModal subscribes to the named teammate's event stream and
// installs a fresh TranscriptView. The returned cmd starts the read loop.
// Caller (handleTranscriptKey) must ensure the modal is currently closed
// and transcriptModalOpenable() returned true.
func (m *Model) openTranscriptModal(agentName string) tea.Cmd {
	if agentName == "" {
		// Pick the first known teammate if none was provided. Sorted so
		// repeated Ctrl+O always lands on the same teammate.
		names := knownAgentNames(m.config.TeammateEvents)
		if len(names) == 0 {
			return nil
		}
		agentName = names[0]
	}

	view := NewTranscriptView(modalTitleFor(m.config.TeammateEvents, agentName))
	view.SetSize(m.Width, m.Height)
	view.SetStatus("Esc to close · Tab to cycle · j/k/PgUp/PgDn to scroll")

	history, ch, cancel := m.config.TeammateEvents.Subscribe(agentName)
	for _, ev := range history {
		view.HandleEvent(ev)
	}

	m.TranscriptModal = view
	m.TranscriptAgent = agentName
	m.transcriptUnsubscribe = cancel

	return waitForTranscriptEvent(agentName, ch)
}

// switchTranscriptAgent tears down the current subscription and starts a
// fresh one against a different teammate. The view is reset because tool
// IDs and streaming state don't carry across teammates.
func (m *Model) switchTranscriptAgent(agentName string) tea.Cmd {
	if m.TranscriptModal == nil || agentName == m.TranscriptAgent {
		return nil
	}
	if m.transcriptUnsubscribe != nil {
		m.transcriptUnsubscribe()
	}
	view := NewTranscriptView(modalTitleFor(m.config.TeammateEvents, agentName))
	view.SetSize(m.Width, m.Height)
	view.SetStatus("Esc to close · Tab to cycle · j/k/PgUp/PgDn to scroll")
	history, ch, cancel := m.config.TeammateEvents.Subscribe(agentName)
	for _, ev := range history {
		view.HandleEvent(ev)
	}
	m.TranscriptAgent = agentName
	m.transcriptUnsubscribe = cancel
	m.TranscriptModal = view
	return waitForTranscriptEvent(agentName, ch)
}

// closeTranscriptModal drops the subscription and resets modal state. Safe
// to call when the modal is already closed.
func (m *Model) closeTranscriptModal() {
	if m.transcriptUnsubscribe != nil {
		m.transcriptUnsubscribe()
		m.transcriptUnsubscribe = nil
	}
	m.TranscriptModal = nil
	m.TranscriptAgent = ""
}

// cycleTranscriptAgent advances the modal target by `step` positions (1 for
// Tab, -1 for Shift+Tab) through the sorted known-agent list. Wraps at
// either end. Returns nil cmd if there are no teammates to cycle to.
func (m *Model) cycleTranscriptAgent(step int) tea.Cmd {
	if m.TranscriptModal == nil || m.config.TeammateEvents == nil {
		return nil
	}
	names := knownAgentNames(m.config.TeammateEvents)
	if len(names) <= 1 {
		return nil
	}
	cur := max(slices.Index(names, m.TranscriptAgent), 0)
	next := (cur + step + len(names)) % len(names)
	return m.switchTranscriptAgent(names[next])
}

// handleTranscriptKey intercepts keystrokes when the modal is open. The
// modal is full-screen so it takes precedence over everything except the
// AskUser / Permission dialogs (which can never be active simultaneously
// because both require live agent state the modal pauses). Returns
// (handled=true) for every key once the modal is open — unknown keys are
// silently swallowed so they can't leak into the textarea behind the
// modal.
//
// Open path: when the modal is CLOSED, only Ctrl+O is recognised; everything
// else falls through.
func (m *Model) handleTranscriptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	// While the fleet list holds focus (including split-preview, where a modal
	// is open beneath the list), the list owns the keyboard. Defer entirely so
	// its navigation/switch keys aren't swallowed by the modal scroll handlers.
	if m.FleetFocus {
		return m, nil, false
	}
	// Toggle from closed → open.
	if m.TranscriptModal == nil {
		if msg.String() != "ctrl+o" {
			return m, nil, false
		}
		// No hub wired → the feature is disabled; let the keystroke fall
		// through to whatever else might handle it.
		if m.config.TeammateEvents == nil {
			return m, nil, false
		}
		if !m.transcriptModalOpenable() {
			// Hub exists but no teammate has published yet — leave a hint
			// in the suggestion line so the user knows the key worked but
			// nothing's available, and consume the key (we DID react).
			m.Suggestion = "(no active teammates to observe)"
			return m, nil, true
		}
		return m, m.openTranscriptModal(""), true
	}

	switch msg.String() {
	case "ctrl+o", "esc":
		m.closeTranscriptModal()
		return m, nil, true
	case "ctrl+c":
		// Closing here AND aborting matches the leader's Ctrl+C contract.
		m.closeTranscriptModal()
		if m.Running && m.Driver != nil {
			m.Driver.Abort()
		}
		return m, nil, true
	case "tab":
		return m, m.cycleTranscriptAgent(1), true
	case "shift+tab":
		return m, m.cycleTranscriptAgent(-1), true
	case "up", "k":
		m.TranscriptModal.ScrollUp(1)
		return m, nil, true
	case "down", "j":
		m.TranscriptModal.ScrollDown(1)
		return m, nil, true
	case "pgup", "u":
		m.TranscriptModal.PageUp()
		return m, nil, true
	case "pgdown", "d":
		m.TranscriptModal.PageDown()
		return m, nil, true
	case "g", "home":
		m.TranscriptModal.ScrollUp(1 << 20) // arbitrarily large; viewport clamps
		return m, nil, true
	case "G", "end":
		m.TranscriptModal.GotoBottom()
		return m, nil, true
	}
	// Swallow everything else so the textarea behind the modal doesn't see
	// stray keystrokes.
	return m, nil, true
}

// handleTranscriptEvent feeds a teammate event into the open modal view.
// Discards events for agents the user has switched away from (the hub
// drops subscriptions on switch, but a final in-flight event can race
// the cancel).
func (m *Model) handleTranscriptEvent(msg TranscriptEventMsg, pending <-chan agentcore.Event) (tea.Model, tea.Cmd) {
	if m.TranscriptModal == nil || msg.Agent != m.TranscriptAgent {
		// Modal closed or target switched after this msg was queued —
		// drop it and stop polling (pending may have been closed by the
		// unsubscribe but the channel send for this msg landed first).
		return m, nil
	}
	m.TranscriptModal.HandleEvent(msg.Event)
	return m, waitForTranscriptEvent(msg.Agent, pending)
}

// waitForTranscriptEvent returns a cmd that reads one event from the hub's
// subscription channel. The result is fed back into Update as either a
// TranscriptEventMsg (normal) or a TranscriptChannelClosedMsg (subscription
// dropped). Update's handler reschedules another read if the modal is
// still open, keeping the read loop alive.
//
// agentName is captured in the closure so the resulting msg carries
// provenance for the "did the user switch teammates while a read was
// in-flight?" check.
func waitForTranscriptEvent(agentName string, ch <-chan agentcore.Event) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return TranscriptChannelClosedMsg{Agent: agentName}
		}
		return transcriptEventEnvelope{Msg: TranscriptEventMsg{Agent: agentName, Event: ev}, Ch: ch}
	}
}

// transcriptEventEnvelope smuggles the channel reference alongside the
// event so Update can re-arm the read without a separate side table from
// agent name → channel. Keeping the channel in the msg means the modal's
// lifecycle and the read loop's lifecycle stay locally reasoned-about.
type transcriptEventEnvelope struct {
	Msg TranscriptEventMsg
	Ch  <-chan agentcore.Event
}

// transcriptViewBody returns the modal's rendered output for View().
// Returns "" when no modal is open so callers can fall back to the normal
// view path.
//
// Before each render we refresh the live-badge: a spinner frame while the
// teammate is still publishing (so the user knows it's working), or a
// static ✓ once it has ended. We piggyback on the leader's m.Spinner —
// it's already tick'd by spinner.TickMsg in update.go, so the modal
// inherits the same cadence without spawning a second ticker.
func (m *Model) transcriptViewBody() string {
	if m.TranscriptModal == nil {
		return ""
	}
	m.TranscriptModal.SetLiveBadge(m.transcriptLiveBadge())
	return m.TranscriptModal.View()
}

// transcriptLiveBadge returns the badge shown at the head of the modal
// status line. Empty when no agent is selected, a styled spinner frame
// while the teammate is active, or a styled "✓ ended" when it has stopped.
func (m *Model) transcriptLiveBadge() string {
	if m.TranscriptAgent == "" {
		return ""
	}
	if m.config.TeammateEvents != nil && m.config.TeammateEvents.IsActive(m.TranscriptAgent) {
		return CommandStyle.Render(m.Spinner.View() + " running")
	}
	return MutedStyle.Render("✓ ended")
}

// transcriptOnResize forwards a window-size change to the open modal so
// the viewport re-wraps. No-op when the modal is closed.
func (m *Model) transcriptOnResize() {
	if m.TranscriptModal == nil {
		return
	}
	m.TranscriptModal.SetSize(m.Width, m.Height)
}
