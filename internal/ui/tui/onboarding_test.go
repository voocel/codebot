package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func press(t *testing.T, m *onboardModel, key tea.KeyMsg) *onboardModel {
	t.Helper()
	next, _ := m.Update(key)
	res, ok := next.(*onboardModel)
	if !ok {
		t.Fatalf("Update returned %T, want *onboardModel", next)
	}
	return res
}

func typeText(t *testing.T, m *onboardModel, s string) *onboardModel {
	t.Helper()
	return press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
}

func keyOf(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestOnboardingProviderNavigation(t *testing.T) {
	m := newOnboardModel()
	if m.step != stepProvider || m.cursor != 0 {
		t.Fatalf("initial state step=%v cursor=%d", m.step, m.cursor)
	}
	m = press(t, m, keyOf("down"))
	m = press(t, m, keyOf("down"))
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.cursor)
	}
	m = press(t, m, keyOf("enter"))
	if m.step != stepModel {
		t.Fatalf("step = %v, want stepModel", m.step)
	}
	m = typeText(t, m, "gemini-3.0-pro")
	m = press(t, m, keyOf("enter"))
	if m.step != stepAPIKey {
		t.Fatalf("step = %v, want stepAPIKey", m.step)
	}
	m = press(t, m, keyOf("esc"))
	if m.step != stepModel {
		t.Fatalf("esc from key should return to model step, got %v", m.step)
	}
	m = press(t, m, keyOf("esc"))
	if m.step != stepProvider {
		t.Fatalf("esc from model should return to provider step, got %v", m.step)
	}
}

func TestOnboardingModelRequiredNoPrefill(t *testing.T) {
	m := newOnboardModel()
	m = press(t, m, keyOf("down")) // Anthropic
	m = press(t, m, keyOf("enter"))
	if m.model != "" {
		t.Fatalf("model must never be prefilled, got %q", m.model)
	}

	// Empty model must not advance.
	m = press(t, m, keyOf("enter"))
	if m.step != stepModel || m.errMsg == "" {
		t.Fatalf("empty model should stay with error, step=%v err=%q", m.step, m.errMsg)
	}

	// A typed id survives a round-trip to the key step and back.
	m = typeText(t, m, "claude-opus-4-8")
	m = press(t, m, keyOf("enter"))
	m = press(t, m, keyOf("esc"))
	if m.step != stepModel || m.model != "claude-opus-4-8" {
		t.Fatalf("typed model lost on esc round-trip: step=%v model=%q", m.step, m.model)
	}

	// Same provider re-entry keeps the typed id; a different provider clears it.
	m = press(t, m, keyOf("esc"))
	m = press(t, m, keyOf("enter"))
	if m.model != "claude-opus-4-8" {
		t.Fatalf("same-provider re-entry should keep model, got %q", m.model)
	}
	m = press(t, m, keyOf("esc"))
	m.cursor = 0 // OpenAI
	m = press(t, m, keyOf("enter"))
	if m.model != "" {
		t.Fatalf("provider switch should clear the model buffer, got %q", m.model)
	}
}

func TestOnboardingKeyMaskingAndValidation(t *testing.T) {
	m := newOnboardModel()
	m.width = 80
	m = press(t, m, keyOf("enter")) // pick first provider
	m = typeText(t, m, "gpt-5-mini")
	m = press(t, m, keyOf("enter")) // model step

	// Empty key must not save, must show an inline error, must not quit.
	m = press(t, m, keyOf("enter"))
	if m.done {
		t.Fatal("empty key should not complete onboarding")
	}
	if m.errMsg == "" {
		t.Fatal("empty key should set an inline error")
	}

	// Long keys show head+tail with the middle masked.
	m = typeText(t, m, "sk-secret-1234567890abcd")
	view := m.View()
	if strings.Contains(view, "sk-secret-1234567890abcd") {
		t.Fatal("API key leaked into the view; middle must be masked")
	}
	if !strings.Contains(view, "sk-s") || !strings.Contains(view, "abcd") {
		t.Fatal("mask should reveal the first and last four characters")
	}
	if !strings.Contains(view, "•") {
		t.Fatal("masked middle should render as dots")
	}

	// Short keys stay fully masked.
	m.key = ""
	m = typeText(t, m, "sk-tiny")
	if v := m.View(); strings.Contains(v, "sk-t") {
		t.Fatal("short keys must not reveal any characters")
	}

	// Pasted whitespace/newlines are stripped from the buffer.
	m.key = ""
	m = typeText(t, m, "sk-ab\n cd\t")
	if m.key != "sk-abcd" {
		t.Fatalf("key buffer = %q, want whitespace stripped", m.key)
	}

	// NUL and control runes (Windows console events) must never enter buffers.
	m.key = ""
	m = typeText(t, m, "\x00\x00sk\x00-live\x1b")
	if m.key != "sk-live" {
		t.Fatalf("key buffer = %q, want control runes stripped", m.key)
	}
}

func TestOnboardingInputFieldChrome(t *testing.T) {
	m := newOnboardModel()
	m.width = 80
	m = press(t, m, keyOf("enter"))
	// Focused text inputs always show a block cursor.
	if !strings.Contains(m.View(), "█") {
		t.Fatal("model step should render a block cursor")
	}
	m = typeText(t, m, "gpt-5")
	if !strings.Contains(m.View(), "█") {
		t.Fatal("cursor should follow typed text")
	}

	// Custom form buffers get the same sanitization as the key field.
	c := newOnboardModel()
	c.cursor = len(c.rows) - 1
	c = press(t, c, keyOf("enter"))
	c = typeText(t, c, "\x00my\x00-proxy ")
	if c.name != "my-proxy" {
		t.Fatalf("name buffer = %q, want sanitized", c.name)
	}
}

func TestOnboardingCustomFlow(t *testing.T) {
	m := newOnboardModel()
	m.cursor = len(m.rows) - 1 // Custom entry
	m = press(t, m, keyOf("enter"))
	if m.step != stepCustom {
		t.Fatalf("step = %v, want stepCustom", m.step)
	}

	// Enter walks fields; empty name on the last field bounces back with error.
	m = press(t, m, keyOf("enter"))
	m = press(t, m, keyOf("enter"))
	m = press(t, m, keyOf("enter"))
	if m.step != stepCustom || m.errMsg == "" || m.field != customName {
		t.Fatalf("empty name should stay on stepCustom with error, got step=%v field=%d err=%q", m.step, m.field, m.errMsg)
	}

	m = typeText(t, m, "my-proxy")
	m = press(t, m, keyOf("enter"))
	m = press(t, m, keyOf("enter"))
	m = typeText(t, m, "http://localhost:8080/v1")
	m = press(t, m, keyOf("enter"))
	if m.step != stepModel {
		t.Fatalf("step = %v, want stepModel", m.step)
	}
	if m.name != "my-proxy" || m.baseURL != "http://localhost:8080/v1" {
		t.Fatalf("custom fields lost: name=%q url=%q", m.name, m.baseURL)
	}
	m = typeText(t, m, "local-model")
	m = press(t, m, keyOf("enter"))
	if m.step != stepAPIKey {
		t.Fatalf("step = %v, want stepAPIKey", m.step)
	}
	// esc chain: key → model → custom form, not straight to the provider list.
	m = press(t, m, keyOf("esc"))
	if m.step != stepModel {
		t.Fatalf("esc should return to stepModel, got %v", m.step)
	}
	m = press(t, m, keyOf("esc"))
	if m.step != stepCustom {
		t.Fatalf("esc should return to stepCustom, got %v", m.step)
	}
}

func TestOnboardingCancel(t *testing.T) {
	for _, key := range []string{"esc", "ctrl+c"} {
		m := newOnboardModel()
		m = press(t, m, keyOf(key))
		if !m.done || m.result.Saved {
			t.Fatalf("%s on provider step should cancel without saving", key)
		}
		if m.View() != "" {
			t.Fatalf("cancelled final view should be empty, got %q", m.View())
		}
	}
}

// TestOnboardingFrameAlignment locks the card invariant: every framed line
// renders at the same display width, so styled segments can't break the box.
func TestOnboardingFrameAlignment(t *testing.T) {
	for _, step := range []onboardStep{stepProvider, stepCustom, stepModel, stepAPIKey} {
		m := newOnboardModel()
		m.width = 80
		m.step = step
		m.errMsg = "some error to render"
		var cardWidth int
		for _, line := range strings.Split(m.View(), "\n") {
			if !strings.ContainsAny(line, "╭│╰") {
				continue
			}
			w := lipgloss.Width(line)
			if cardWidth == 0 {
				cardWidth = w
				continue
			}
			if w != cardWidth {
				t.Fatalf("step=%v: frame line width %d != %d\nline: %q", step, w, cardWidth, line)
			}
		}
		if cardWidth == 0 {
			t.Fatalf("step=%v: no framed lines found", step)
		}
	}
}

func TestOnboardingViewRendersAtAnyWidth(t *testing.T) {
	for _, w := range []int{0, 40, 80, 200} {
		for _, step := range []onboardStep{stepProvider, stepCustom, stepModel, stepAPIKey} {
			m := newOnboardModel()
			m.width = w
			m.step = step
			if v := m.View(); !strings.Contains(v, "codebot") {
				t.Fatalf("width=%d step=%v: view missing card title", w, step)
			}
		}
	}
}
