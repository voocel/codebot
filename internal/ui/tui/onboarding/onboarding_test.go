package onboarding

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func keyPress(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

func press(w *wizard, keys ...string) {
	for _, k := range keys {
		w.Update(keyPress(k))
	}
}

func write(w *wizard, text string) {
	for _, r := range text {
		press(w, string(r))
	}
}

func screen(w *wizard) string { return ansi.Strip(w.View().Content) }

func settings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSetUpAProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	w := newWizard()
	w.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if s := screen(w); !strings.Contains(s, "Choose your model provider") || !strings.Contains(s, "Anthropic") {
		t.Fatalf("first step:\n%s", s)
	}

	press(w, "2")
	if !strings.Contains(screen(w), "Which model") {
		t.Fatalf("2 did not pick Anthropic:\n%s", screen(w))
	}
	press(w, "enter")
	if !strings.Contains(screen(w), "A model id is required") {
		t.Errorf("an empty model went through:\n%s", screen(w))
	}
	write(w, "claude-x")
	press(w, "enter")

	// The key is masked, and a pasted one loses its newline.
	w.Update(tea.PasteMsg{Content: "sk-secret\n"})
	if s := screen(w); strings.Contains(s, "sk-secret") || !strings.Contains(s, "•••") {
		t.Errorf("the key shows:\n%s", s)
	}
	press(w, "enter")

	if !w.result.Saved || w.result.Provider != "Anthropic" || w.result.Model != "claude-x" {
		t.Fatalf("result = %+v", w.result)
	}
	s := settings(t, w.result.Path)
	if s["provider"] != "anthropic" || s["model"] != "claude-x" {
		t.Errorf("saved %v", s)
	}
	if !strings.Contains(screen(w), "Anthropic is set up") {
		t.Errorf("last frame:\n%s", screen(w))
	}
}

func TestSetUpACustomProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	w := newWizard()
	press(w, "6")
	press(w, "enter", "enter", "enter")
	if !strings.Contains(screen(w), "A name is required") {
		t.Fatalf("an empty name went through:\n%s", screen(w))
	}
	write(w, "local")
	press(w, "tab", "right")
	protocol := w.protocols[w.protocol]
	press(w, "tab")
	write(w, "http://localhost:8080/v1")
	press(w, "enter")
	write(w, "qwen")
	press(w, "enter")
	write(w, "key")
	press(w, "enter")

	if !w.result.Saved || w.result.Provider != "local" {
		t.Fatalf("result = %+v", w.result)
	}
	p := settings(t, w.result.Path)["providers"].(map[string]any)["local"].(map[string]any)
	if p["base_url"] != "http://localhost:8080/v1" || p["type"] != protocol {
		t.Errorf("saved %v", p)
	}
}

func TestBackKeepsTheModelForTheSameProvider(t *testing.T) {
	w := newWizard()
	press(w, "1")
	write(w, "gpt-x")
	press(w, "esc", "enter")
	if got := w.model.Value(); got != "gpt-x" {
		t.Errorf("the model went: %q", got)
	}
	press(w, "enter")
	write(w, "sk-openai")
	press(w, "esc", "esc", "down", "enter")
	if got := w.model.Value(); got != "" {
		t.Errorf("another provider kept the model %q", got)
	}
	if got := w.key.Value(); got != "" {
		t.Errorf("another provider kept the key %q", got)
	}
}
