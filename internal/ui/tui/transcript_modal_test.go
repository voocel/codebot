package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/interact"
)

// modalTestModel returns a Model wired with a fresh hub, no driver, and a
// reasonable viewport size — enough for the modal subsystem to exercise its
// open/close/cycle paths.
func modalTestModel(t *testing.T) (*Model, *app.AgentHub) {
	t.Helper()
	hub := app.NewAgentHub()
	m := testModel("test-model")
	m.agents = hub
	m.Ready = true
	m.Width = 100
	m.Height = 30
	return m, hub
}

// keyMsg is a shorthand for building a tea.KeyMsg with a string label
// matching the cases in handleKey.
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "ctrl+o":
		return tea.KeyMsg{Type: tea.KeyCtrlO}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "j":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	case "k":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	default:
		panic("unknown key: " + s)
	}
}

func TestTranscriptModal_CtrlONoAgentsHintsButDoesNotOpen(t *testing.T) {
	m, _ := modalTestModel(t)
	// Hub has no active agents yet.
	_, _, handled := m.handleTranscriptKey(keyMsg("ctrl+o"))
	if !handled {
		t.Fatal("ctrl+o should be handled (closed → tries to open)")
	}
	if m.TranscriptModal != nil {
		t.Error("modal opened despite no active agents")
	}
	if m.Suggestion != "" {
		t.Errorf("the hint must not become a suggestion Enter would send: %q", m.Suggestion)
	}
	if len(m.Scrollback) == 0 || !strings.Contains(m.Scrollback[len(m.Scrollback)-1], "No background agents") {
		t.Errorf("expected the hint in the scrollback, got: %q", m.Scrollback)
	}
}

func TestTranscriptModal_CtrlOOpensWhenAgentActive(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("researcher", agentcore.MessageStart{})

	_, _, handled := m.handleTranscriptKey(keyMsg("ctrl+o"))
	if !handled {
		t.Fatal("ctrl+o not handled")
	}
	if m.TranscriptModal == nil {
		t.Fatal("modal did not open")
	}
	if m.TranscriptAgent != "researcher" {
		t.Errorf("modal agent = %q, want researcher", m.TranscriptAgent)
	}
}

func TestTranscriptModal_EscClosesModal(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("researcher", agentcore.MessageStart{})
	m.handleTranscriptKey(keyMsg("ctrl+o"))
	if m.TranscriptModal == nil {
		t.Fatal("setup failed: modal did not open")
	}

	_, _, handled := m.handleTranscriptKey(keyMsg("esc"))
	if !handled {
		t.Fatal("esc not handled while modal open")
	}
	if m.TranscriptModal != nil {
		t.Error("esc did not close modal")
	}
	if m.TranscriptAgent != "" {
		t.Error("agent name not cleared on close")
	}
}

func TestTranscriptModal_CtrlOToggles(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("researcher", agentcore.MessageStart{})
	m.handleTranscriptKey(keyMsg("ctrl+o"))
	m.handleTranscriptKey(keyMsg("ctrl+o"))
	if m.TranscriptModal != nil {
		t.Error("second ctrl+o did not toggle modal closed")
	}
}

func TestTranscriptModal_TabCyclesAcrossAgents(t *testing.T) {
	m, hub := modalTestModel(t)
	// Publish two agents so cycling is meaningful.
	hub.Publish("alice", agentcore.MessageStart{})
	hub.Publish("bob", agentcore.MessageStart{})

	m.handleTranscriptKey(keyMsg("ctrl+o"))
	if m.TranscriptAgent != "alice" {
		t.Fatalf("initial agent = %q, want alice (first sorted)", m.TranscriptAgent)
	}

	m.handleTranscriptKey(keyMsg("tab"))
	if m.TranscriptAgent != "bob" {
		t.Errorf("after tab = %q, want bob", m.TranscriptAgent)
	}
	m.handleTranscriptKey(keyMsg("tab"))
	if m.TranscriptAgent != "alice" {
		t.Errorf("after second tab = %q, want alice (wrap)", m.TranscriptAgent)
	}
	m.handleTranscriptKey(keyMsg("shift+tab"))
	if m.TranscriptAgent != "bob" {
		t.Errorf("after shift+tab = %q, want bob", m.TranscriptAgent)
	}
}

func TestTranscriptModal_ScrollKeysAreSwallowed(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("researcher", agentcore.MessageStart{})
	m.handleTranscriptKey(keyMsg("ctrl+o"))
	if m.TranscriptModal == nil {
		t.Fatal("setup failed")
	}

	for _, k := range []string{"j", "k"} {
		_, _, handled := m.handleTranscriptKey(keyMsg(k))
		if !handled {
			t.Errorf("scroll key %q not handled by modal", k)
		}
	}
}

func TestTranscriptModal_ViewTakesOverWhenOpen(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("researcher", agentcore.MessageStart{})
	m.handleTranscriptKey(keyMsg("ctrl+o"))

	body := m.View()
	if !strings.Contains(body, "agent: researcher") {
		t.Errorf("View() did not include modal title; got: %q", body)
	}
}

func TestTranscriptModal_ViewFallsBackWhenClosed(t *testing.T) {
	m, _ := modalTestModel(t)
	body := m.View()
	if strings.Contains(body, "agent:") {
		t.Errorf("View() leaked modal content while closed: %q", body)
	}
}

func TestTranscriptModal_HandleEventRoutesToOpenModal(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("researcher", agentcore.MessageStart{})
	m.handleTranscriptKey(keyMsg("ctrl+o"))
	if m.TranscriptModal == nil {
		t.Fatal("setup failed: modal not open")
	}

	// Feed an assistant message through the Update path directly. We don't
	// run the cmd loop here — we just check the modal received the event.
	msg := TranscriptEventMsg{
		Agent: "researcher",
		Event: agentcore.MessageEnd{Message: asstMsg("hello from modal")},
	}
	m.handleTranscriptEvent(msg, nil)

	body := m.View()
	if !strings.Contains(body, "hello from modal") {
		t.Errorf("modal did not render assistant message; View()=%q", body)
	}
}

func TestTranscriptModal_StoppedAgentStillOpensWithHistory(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("researcher", agentcore.MessageStart{})
	hub.Publish("researcher", agentcore.MessageEnd{Message: asstMsg("final answer")})
	hub.MarkStopped("researcher")

	_, _, handled := m.handleTranscriptKey(keyMsg("ctrl+o"))
	if !handled || m.TranscriptModal == nil {
		t.Fatalf("ctrl+o on stopped agent did not open modal (handled=%v modal=%v)", handled, m.TranscriptModal)
	}
	body := m.View()
	if !strings.Contains(body, "final answer") {
		t.Errorf("history replay missing assistant message; View()=%q", body)
	}
	if !strings.Contains(body, "(ended)") {
		t.Errorf("title should mark stopped agent as ended; View()=%q", body)
	}
}

func TestTranscriptModal_TabCyclesAcrossStoppedAgents(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("alice", agentcore.MessageStart{})
	hub.Publish("bob", agentcore.MessageStart{})
	hub.MarkStopped("alice") // alice ended, bob still live

	m.handleTranscriptKey(keyMsg("ctrl+o"))
	if m.TranscriptAgent != "alice" {
		t.Fatalf("initial agent = %q, want alice (sorted first, even though stopped)", m.TranscriptAgent)
	}
	m.handleTranscriptKey(keyMsg("tab"))
	if m.TranscriptAgent != "bob" {
		t.Errorf("after tab = %q, want bob", m.TranscriptAgent)
	}
}

func TestTranscriptModal_StaleEventForOldAgentIsDropped(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("alice", agentcore.MessageStart{})
	hub.Publish("bob", agentcore.MessageStart{})

	m.handleTranscriptKey(keyMsg("ctrl+o")) // alice
	m.handleTranscriptKey(keyMsg("tab"))    // bob

	// In-flight event for alice arriving after we switched should not
	// touch bob's view.
	msg := TranscriptEventMsg{
		Agent: "alice",
		Event: agentcore.MessageEnd{Message: asstMsg("stale-content")},
	}
	m.handleTranscriptEvent(msg, nil)

	body := m.View()
	if strings.Contains(body, "stale-content") {
		t.Errorf("stale event leaked into bob's view: %q", body)
	}
}

// A dialog takes the keys first, so one arriving while the modal is open
// closes it: otherwise keys meant to scroll would answer a dialog nobody sees.
func TestTranscriptModal_DialogClosesModal(t *testing.T) {
	m, hub := modalTestModel(t)
	hub.Publish("researcher", agentcore.MessageStart{})
	m.handleTranscriptKey(keyMsg("ctrl+o"))
	if m.TranscriptModal == nil {
		t.Fatal("setup failed: modal did not open")
	}

	m.Update(PermissionMsg{Approval: interact.Approval{Tool: "bash", Summary: "rm -rf build"}, RespCh: make(chan interact.Choice, 1)})
	if m.TranscriptModal != nil {
		t.Fatal("a permission dialog must close the modal")
	}
	if !strings.Contains(m.View(), "rm -rf build") {
		t.Fatalf("the dialog must be visible:\n%s", m.View())
	}
}
