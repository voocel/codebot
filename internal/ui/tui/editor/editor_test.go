package editor

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newEditor() *Editor {
	e := New(func() []Completion {
		return []Completion{{Name: "help", Run: true}, {Name: "model", Aliases: []string{"m"}, Run: true}, {Name: "btw"}}
	})
	e.SetWidth(60)
	return e
}

func keyPress(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "shift+enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

// press presses keys and returns the input sent, if any.
func press(e *Editor, keys ...string) *Input {
	var in *Input
	for _, k := range keys {
		if _, sent := e.Update(keyPress(k)); sent != nil {
			in = sent
		}
	}
	return in
}

func write(e *Editor, text string) {
	for _, r := range text {
		press(e, string(r))
	}
}

func TestSend(t *testing.T) {
	e := newEditor()
	write(e, "hi")
	press(e, "shift+enter")
	write(e, "there")
	in := press(e, "enter")
	if in == nil || in.Text != "hi\nthere" {
		t.Fatalf("sent %+v", in)
	}
	if !e.Empty() {
		t.Error("the editor kept the input")
	}
	if press(e, "enter") != nil {
		t.Error("an empty input was sent")
	}
}

func TestLongPasteGoesInAsAReference(t *testing.T) {
	e := newEditor()
	body := strings.Repeat("x", 600) + "\n" + strings.Repeat("y", 600) + "\nz"
	e.Update(tea.PasteMsg{Content: body})
	if got := e.ta.Value(); got != "[Pasted text #1 +3 lines]" {
		t.Fatalf("the input holds %q", got)
	}
	write(e, " ok")
	if in := press(e, "enter"); in == nil || in.Text != body+" ok" {
		t.Errorf("sent %+v", in)
	}

	// Backspace takes a reference whole.
	e.Update(tea.PasteMsg{Content: body})
	press(e, "backspace")
	if !e.Empty() {
		t.Errorf("the input holds %q", e.ta.Value())
	}
}

func TestCompletion(t *testing.T) {
	e := newEditor()
	write(e, "/")
	if got := len(e.Menu(60)); got != 3 {
		t.Fatalf("the menu shows %d commands", got)
	}
	press(e, "down", "tab")
	if got := e.ta.Value(); got != "/model " {
		t.Errorf("tab completed to %q", got)
	}
	if e.Menu(60) != nil {
		t.Error("the menu stayed")
	}

	// The menu starts over on the best match.
	e.Clear()
	write(e, "/")
	press(e, "down", "down")
	e.Clear()
	write(e, "/")
	if c, _ := e.selected(); c.Name != "help" {
		t.Errorf("a new menu selects %q", c.Name)
	}
	press(e, "down")
	write(e, "m")
	if c, _ := e.selected(); c.Name != "model" {
		t.Errorf("/m selects %q", c.Name)
	}

	// enter runs a command that takes no arguments.
	e.Clear()
	write(e, "/he")
	if in := press(e, "enter"); in == nil || in.Text != "/help" {
		t.Errorf("sent %+v", in)
	}

	// and completes one that does.
	write(e, "/bt")
	if in := press(e, "enter"); in != nil || e.ta.Value() != "/btw " {
		t.Errorf("sent %+v, input %q", in, e.ta.Value())
	}
}

func TestHistory(t *testing.T) {
	e := newEditor()
	e.SetHistory(NewHistory(filepath.Join(t.TempDir(), "history.jsonl"), "/project"))
	for _, s := range []string{"one", "two"} {
		write(e, s)
		press(e, "enter")
	}
	write(e, "draft")
	for _, c := range []struct{ key, want string }{
		{"up", "two"}, {"up", "one"}, {"up", "one"}, {"down", "two"}, {"down", "draft"},
	} {
		press(e, c.key)
		if got := e.ta.Value(); got != c.want {
			t.Errorf("%s: the input holds %q, want %q", c.key, got, c.want)
		}
	}
}

func TestSuggestion(t *testing.T) {
	e := newEditor()
	e.SetSuggestion("run the tests")
	press(e, "tab")
	if got := e.ta.Value(); got != "run the tests" {
		t.Errorf("tab took %q", got)
	}

	e.Clear()
	e.SetSuggestion("run the tests")
	if in := press(e, "enter"); in == nil || in.Text != "run the tests" {
		t.Errorf("enter sent %+v", in)
	}

	e.SetSuggestion("run the tests")
	write(e, "no")
	if in := press(e, "enter"); in == nil || in.Text != "no" {
		t.Errorf("typing did not replace the suggestion: %+v", in)
	}
}

func TestUpMovesWithinAWrappedLine(t *testing.T) {
	e := newEditor()
	e.SetWidth(30)
	e.SetHistory(NewHistory(filepath.Join(t.TempDir(), "history.jsonl"), "/project"))
	write(e, "old")
	press(e, "enter")
	long := strings.Repeat("word ", 16)
	write(e, long)
	press(e, "up")
	if got := e.ta.Value(); got != long {
		t.Errorf("up on a wrapped row recalled %q", got)
	}
}

func TestPastedCarriageReturns(t *testing.T) {
	e := newEditor()
	body := strings.Repeat("x", 600) + "\r" + strings.Repeat("y", 600)
	e.Update(tea.PasteMsg{Content: body})
	if got := e.ta.Value(); got != "[Pasted text #1 +2 lines]" {
		t.Errorf("the input holds %q", got)
	}
	if in := press(e, "enter"); in == nil || strings.Contains(in.Text, "\r") {
		t.Errorf("sent %q", in.Text)
	}
}
