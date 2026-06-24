package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
)

func down() tea.KeyMsg  { return tea.KeyMsg{Type: tea.KeyDown} }
func up() tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyUp} }
func enter() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }
func ctrlF() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyCtrlF} }
func rune_(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestFleet_DownEntersFocusUpExits(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("researcher", agentcore.Event{Type: agentcore.EventAgentStart})

	if _, _, handled := m.handleDownKey(); !handled {
		t.Fatal("↓ at last line with a live agent should be handled")
	}
	// Entry lands on the first agent (row 1); "main" sits above at row 0.
	if !m.FleetFocus || m.FleetCursor != 1 {
		t.Fatalf("expected fleet focus at cursor 1, got focus=%v cursor=%d", m.FleetFocus, m.FleetCursor)
	}
	// Focus moved into the list → the input is blurred (no cursor, no typing).
	if m.Input.Focused() {
		t.Error("input should be blurred while the fleet list holds focus")
	}

	// ↑ moves up onto the "main" row (still focused)...
	if _, _, handled := m.handleFleetKey(up()); !handled {
		t.Fatal("↑ should be handled while focused")
	}
	if !m.FleetFocus || m.FleetCursor != 0 {
		t.Fatalf("↑ should land on main (cursor 0, focused), got focus=%v cursor=%d", m.FleetFocus, m.FleetCursor)
	}
	// ...and ↑ past main returns focus to the input.
	if _, _, handled := m.handleFleetKey(up()); !handled {
		t.Fatal("↑ should be handled while focused")
	}
	if m.FleetFocus {
		t.Error("↑ past the main row should exit fleet focus")
	}
	// Leaving the list re-focuses the input so typing resumes.
	if !m.Input.Focused() {
		t.Error("input should regain focus after leaving the fleet list")
	}
}

func TestFleet_DownNoAgentsDoesNotFocus(t *testing.T) {
	m, _ := modalTestModel(t) // empty hub

	_, _, handled := m.handleDownKey()
	if handled {
		t.Error("↓ with no agents should fall through (not handled)")
	}
	if m.FleetFocus {
		t.Error("must not enter fleet focus with no agents")
	}
}

func TestFleet_NavigateAndSelectOpensTranscript(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})
	hub.Publish("bob", agentcore.Event{Type: agentcore.EventAgentStart})

	m.handleDownKey() // enter focus, cursor 1 = alice (row 0 is main)

	if _, _, handled := m.handleFleetKey(down()); !handled {
		t.Fatal("↓ should move selection")
	}
	if m.FleetCursor != 2 {
		t.Fatalf("cursor = %d, want 2 (bob)", m.FleetCursor)
	}

	_, cmd, handled := m.handleFleetKey(enter())
	if !handled {
		t.Fatal("enter should be handled")
	}
	if cmd == nil {
		t.Error("enter should return a cmd that starts the transcript read loop")
	}
	// Enter opens the preview inline (split mode): focus STAYS in the list so
	// the user can keep switching agents; the modal opens for the selection.
	if !m.FleetFocus {
		t.Error("enter should keep fleet focus (inline split preview)")
	}
	if m.TranscriptModal == nil || m.TranscriptAgent != "bob" {
		t.Fatalf("expected transcript modal open for bob, got agent=%q modal=%v", m.TranscriptAgent, m.TranscriptModal != nil)
	}
}

// TestFleet_PreviewKeepsListAndSwitches covers the unified interaction the user
// asked for: ↑/↓ only move the highlight, Enter confirms. The preview stays put
// during navigation and only switches/returns on Enter — identical whether the
// upper area is the conversation or a transcript.
func TestFleet_PreviewKeepsListAndSwitches(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})
	hub.Publish("bob", agentcore.Event{Type: agentcore.EventAgentStart})

	m.handleDownKey()         // focus, cursor 1 = alice (list-only)
	m.handleFleetKey(enter()) // confirm → preview alice (split)
	if !m.FleetFocus || m.TranscriptAgent != "alice" {
		t.Fatalf("expected split preview for alice, focus=%v agent=%q", m.FleetFocus, m.TranscriptAgent)
	}

	// ↓ only moves the highlight; the preview stays on alice until Enter.
	m.handleFleetKey(down())
	if m.FleetCursor != 2 || m.TranscriptAgent != "alice" {
		t.Fatalf("↓ should move highlight only, cursor=%d agent=%q (want 2, alice)", m.FleetCursor, m.TranscriptAgent)
	}
	// Enter confirms the switch to bob, in place (focus stays in the list).
	m.handleFleetKey(enter())
	if !m.FleetFocus || m.TranscriptAgent != "bob" {
		t.Fatalf("Enter should switch preview to bob, focus=%v agent=%q", m.FleetFocus, m.TranscriptAgent)
	}

	// ↑ all the way onto main only moves the highlight; the preview is NOT torn
	// down by navigation — the user must press Enter on main to return.
	m.handleFleetKey(up()) // → alice row
	m.handleFleetKey(up()) // → main (cursor 0)
	if !m.FleetFocus || m.FleetCursor != 0 || m.TranscriptModal == nil {
		t.Fatalf("navigating to main should keep the preview, cursor=%d focus=%v modal=%v", m.FleetCursor, m.FleetFocus, m.TranscriptModal != nil)
	}

	// Enter on main is what returns to the conversation: exits + closes preview.
	m.handleFleetKey(enter())
	if m.FleetFocus || m.TranscriptModal != nil {
		t.Fatalf("Enter on main should exit and close preview, focus=%v modal=%v", m.FleetFocus, m.TranscriptModal != nil)
	}
}

// TestFleet_SplitRendersDivider verifies the split layout draws a divider
// between the preview pane and the list, with the confirmed agent shown above.
func TestFleet_SplitRendersDivider(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})

	m.handleDownKey()         // cursor 1 = alice
	m.handleFleetKey(enter()) // confirm → split preview

	out := m.renderFleetSplit()
	if !strings.Contains(out, "─") {
		t.Error("split should draw a divider between preview and list")
	}
	if !strings.Contains(out, "alice") {
		t.Error("split should show the previewed agent in the list")
	}
}

func TestFleet_TypingJumpsBackToInput(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("researcher", agentcore.Event{Type: agentcore.EventAgentStart})
	m.handleDownKey() // focus drops into the list (input blurred)

	// handleFleetKey hands a printable key back (handled=false) and re-focuses
	// the input, so the list never swallows typing.
	_, _, handled := m.handleFleetKey(rune_('a'))
	if handled {
		t.Error("a printable key should fall through (handled=false) so it reaches the input")
	}
	if m.FleetFocus {
		t.Error("typing should drop fleet focus")
	}
	if !m.Input.Focused() {
		t.Error("typing should re-focus the input")
	}

	// End-to-end through Update: focus the list again, then type — the character
	// must land in the input box and focus return to it.
	m.handleDownKey()
	m.Update(rune_('h'))
	m.Update(rune_('i'))
	if m.FleetFocus {
		t.Error("typing should leave the fleet list")
	}
	if got := m.Input.Value(); got != "hi" {
		t.Errorf("typed characters should land in the input, got %q want %q", got, "hi")
	}
}

func TestFleet_XStopsSelectedAgent(t *testing.T) {
	hub := agent.NewTeammateEventHub()
	var stopped string
	m := New(nil, "test-model", Config{
		TeammateEvents: hub,
		StopAgent:      func(name string) { stopped = name },
	})
	m.Ready, m.Width, m.Height = true, 100, 30
	hub.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})
	hub.Publish("bob", agentcore.Event{Type: agentcore.EventAgentStart})

	m.handleDownKey()            // focus, cursor 1 = alice (row 0 is main)
	m.handleFleetKey(down())     // cursor 2 = bob
	m.handleFleetKey(rune_('x')) // stop bob

	if stopped != "bob" {
		t.Fatalf("StopAgent called with %q, want bob", stopped)
	}
	if !m.FleetFocus {
		t.Error("x should keep focus in the list to stop others")
	}
}

func TestFleet_CtrlFStopsAll(t *testing.T) {
	hub := agent.NewTeammateEventHub()
	called := false
	m := New(nil, "test-model", Config{
		TeammateEvents: hub,
		StopAllAgents:  func() { called = true },
	})
	m.Ready, m.Width, m.Height = true, 100, 30
	hub.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})

	m.handleDownKey()
	m.handleFleetKey(ctrlF())

	if !called {
		t.Error("ctrl+f should invoke StopAllAgents")
	}
}

func TestFleet_RenderShowsElapsedForActive(t *testing.T) {
	hub := agent.NewTeammateEventHub()
	m := New(nil, "test-model", Config{
		TeammateEvents: hub,
		FleetAgentStat: func(string) (time.Duration, bool) { return 63 * time.Second, true },
	})
	m.Ready, m.Width, m.Height = true, 100, 30
	hub.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})

	out := m.renderFleetList()
	if want := formatDuration(63 * time.Second); !strings.Contains(out, want) {
		t.Errorf("render should show elapsed %q for active agent, got:\n%s", want, out)
	}
}

func TestFleet_RenderListsAgentsWithStatus(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("alice", agentcore.Event{Type: agentcore.EventAgentStart})
	hub.Publish("bob", agentcore.Event{Type: agentcore.EventAgentStart})
	hub.MarkStopped("bob")

	out := m.renderFleetList()
	if !strings.Contains(out, "main") {
		t.Error("render should always include the main row")
	}
	if !strings.Contains(out, "alice") {
		t.Error("render should list active agent alice")
	}
	if !strings.Contains(out, "bob") || !strings.Contains(out, "ended") {
		t.Error("render should list ended agent bob with an (ended) marker")
	}
}
