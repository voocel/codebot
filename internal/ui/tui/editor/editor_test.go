package editor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newEditor() *Editor {
	return newEditorIn("")
}

func newEditorIn(root string) *Editor {
	e := New(func() []Completion {
		return []Completion{{Name: "help", Run: true}, {Name: "model", Aliases: []string{"m"}, Run: true}, {Name: "btw"}}
	}, func() string { return root })
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
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+r":
		return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
}

// press runs the commands each key returns, as the program would, and
// returns the input sent, if any.
func press(e *Editor, keys ...string) *Input {
	var in *Input
	for _, k := range keys {
		cmd, sent := e.Update(keyPress(k))
		if sent != nil {
			in = sent
		}
		do(e, cmd)
	}
	return in
}

func do(e *Editor, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			do(e, c)
		}
	case filesMsg:
		c, _ := e.Update(msg)
		do(e, c)
	}
}

func write(e *Editor, text string) {
	for _, r := range text {
		if r == ' ' {
			press(e, "space")
		} else {
			press(e, string(r))
		}
	}
}

func labels(e *Editor) []string {
	var out []string
	for _, it := range e.menu {
		out = append(out, it.label)
	}
	return out
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

	// Backspace deletes a reference as a whole.
	e.Update(tea.PasteMsg{Content: body})
	press(e, "backspace")
	if !e.Empty() {
		t.Errorf("the input holds %q", e.ta.Value())
	}

	// A carriage return breaks a line and is not sent.
	e = newEditor()
	e.Update(tea.PasteMsg{Content: strings.Repeat("x", 600) + "\r" + strings.Repeat("y", 600)})
	if got := e.ta.Value(); got != "[Pasted text #1 +2 lines]" {
		t.Errorf("the input holds %q", got)
	}
	if in := press(e, "enter"); in == nil || strings.Contains(in.Text, "\r") {
		t.Errorf("sent %+v", in)
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
	if c, _ := e.selected(); c.label != "/help" {
		t.Errorf("a new menu selects %q", c.label)
	}
	press(e, "down")
	write(e, "m")
	if c, _ := e.selected(); c.label != "/model" {
		t.Errorf("/m selects %q", c.label)
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

	// On a wrapped row, up moves within the line.
	e.Clear()
	e.SetWidth(30)
	long := strings.Repeat("word ", 16)
	write(e, long)
	press(e, "up")
	if got := e.ta.Value(); got != long {
		t.Errorf("up on a wrapped row recalled %q", got)
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

func TestMentionsAFile(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"src/editor.go", "src/view.go", "README.md", ".git/config", "docs/my notes.md"} {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e := newEditorIn(root)

	write(e, "look at @")
	if got, want := labels(e), []string{"src/", "docs/", "README.md", "src/view.go", "src/editor.go", "docs/my notes.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("@ offers %q, want %q", got, want)
	}
	write(e, "edgo")
	if got := labels(e); len(got) != 1 || got[0] != "src/editor.go" {
		t.Fatalf("@edgo offers %q", got)
	}
	if in := press(e, "enter"); in != nil || e.ta.Value() != "look at @src/editor.go " {
		t.Fatalf("sent %+v, the input holds %q", in, e.ta.Value())
	}
	if e.Menu(60) != nil {
		t.Error("the menu stayed after the mention")
	}

	// Picking a directory lists its contents.
	write(e, "and @sr")
	press(e, "tab")
	if got, want := labels(e), []string{"src/view.go", "src/editor.go"}; e.ta.Value() != "look at @src/editor.go and @src/" || !reflect.DeepEqual(got, want) {
		t.Fatalf("the input holds %q, the menu %q", e.ta.Value(), got)
	}

	// A path with a space is quoted.
	e.Clear()
	write(e, "@notes")
	press(e, "tab")
	if got := e.ta.Value(); got != `@"docs/my notes.md" ` {
		t.Errorf("the input holds %q", got)
	}

	// An email address is not a mention.
	e.Clear()
	write(e, "me@ex")
	if e.Menu(60) != nil {
		t.Error("an address opened the menu")
	}
}

func TestRank(t *testing.T) {
	paths := []string{"internal/", "internal/ui/", "internal/ui/view.go", "internal/ui/editor/", "internal/ui/editor/editor.go", "internal/ui/editor/editor_test.go", "docs/editing.md"}
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"editor.go", []string{"internal/ui/editor/editor.go", "internal/ui/editor/editor_test.go"}},
		{"Editor", []string{"internal/ui/editor/", "internal/ui/editor/editor.go", "internal/ui/editor/editor_test.go"}},
		{"ui/v", []string{"internal/ui/view.go"}},
		{"edtst", []string{"internal/ui/editor/editor_test.go"}},
		{"zz", nil},
	} {
		if got := rank(paths, c.q); !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("%q ranks %q, want %q", c.q, got, c.want)
		}
	}
}

func TestSearchesTheHistory(t *testing.T) {
	e := newEditor()
	e.SetHistory(NewHistory(filepath.Join(t.TempDir(), "history.jsonl"), "/project"))
	for _, s := range []string{"fix the tests", "write the docs", "run the tests"} {
		write(e, s)
		press(e, "enter")
	}

	write(e, "draft")
	press(e, "ctrl+r")
	if !strings.Contains(e.View(60), "Search history") || len(e.menu) != 3 {
		t.Fatalf("ctrl+r shows %d entries:\n%s", len(e.menu), e.View(60))
	}
	write(e, "TESTS")
	if got, want := labels(e), []string{"run the tests", "fix the tests"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the search offers %q", got)
	}
	press(e, "ctrl+r")
	if in := press(e, "enter"); in != nil || e.ta.Value() != "fix the tests" {
		t.Fatalf("sent %+v, the input holds %q", in, e.ta.Value())
	}
	if strings.Contains(e.View(60), "Search history") {
		t.Error("the search went on after picking")
	}

	// esc restores the input.
	e.Clear()
	write(e, "draft")
	press(e, "ctrl+r")
	write(e, "nothing like it")
	if menu := e.Menu(60); len(menu) != 1 || !strings.Contains(menu[0], "Nothing sent before") {
		t.Errorf("a search finding nothing shows %q", menu)
	}
	if in := press(e, "enter"); in != nil {
		t.Errorf("enter sent the search: %+v", in)
	}
	if !e.Dismiss() || e.ta.Value() != "draft" {
		t.Errorf("esc left %q", e.ta.Value())
	}
}
