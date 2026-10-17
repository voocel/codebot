package editor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/ui/tui/theme"
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
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	if c, ok := strings.CutPrefix(k, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: []rune(c)[0], Mod: tea.ModCtrl}
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

// Text sent and put back, as when a run stops, comes back as it went in:
// its pastes as references, not a body too long to edit.
func TestInsertFoldsPastesBack(t *testing.T) {
	e := newEditor()
	body := strings.Repeat("x", 600) + "\n" + strings.Repeat("y", 600)
	e.Update(tea.PasteMsg{Content: body})
	write(e, " fix this")
	in := press(e, "enter")
	e.Insert(in.Text)
	if got := e.ta.Value(); got != "[Pasted text #1 +2 lines] fix this" {
		t.Fatalf("the input holds %q", got)
	}
	if again := press(e, "enter"); again == nil || again.Text != in.Text {
		t.Fatalf("sent %+v, want %q", again, in.Text)
	}

	// Text with no paste it knows goes in as one when too long to edit.
	long := strings.Repeat("z", pasteInline+1)
	e.Insert(long)
	if got := e.ta.Value(); got != "[Pasted text #2]" {
		t.Fatalf("the input holds %q", got)
	}
	if again := press(e, "enter"); again == nil || again.Text != long {
		t.Fatal("did not send the text back")
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

// The command the input starts with stands out; a path, an unknown name and
// the query of a search do not.
func TestMarksTheCommand(t *testing.T) {
	e := newEditor()
	for in, want := range map[string]int{"/model": 6, "/M opus": 2, "/btw\nmore": 4, "/usr/bin": 0, "/typo": 0, "/": 0, "model": 0} {
		e.ta.SetValue(in)
		if got := e.command(); got != want {
			t.Errorf("%q marks %d columns, want %d", in, got, want)
		}
	}

	e.ta.SetValue("/model opus")
	view := e.View(60)
	if !strings.Contains(view, theme.WarmText.Render("/model")) || !strings.Contains(ansi.Strip(view), "❯ /model opus") {
		t.Errorf("the command is not marked:\n%q", view)
	}

	e.Clear()
	e.SetHistory(NewHistory(filepath.Join(t.TempDir(), "history.jsonl"), "/project"))
	press(e, "ctrl+r")
	write(e, "/model")
	if e.command() != 0 {
		t.Error("a search's query is marked")
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
		{"up", "two"}, {"ctrl+p", "one"}, {"up", "one"}, {"ctrl+n", "two"}, {"down", "draft"},
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

func TestDiscardKeepsTheInput(t *testing.T) {
	e := newEditor()
	e.SetHistory(NewHistory(filepath.Join(t.TempDir(), "history.jsonl"), "/project"))
	write(e, "sent")
	press(e, "enter")

	write(e, "half typed")
	e.Discard()
	if !e.Empty() {
		t.Fatalf("discard left %q", e.ta.Value())
	}
	for _, want := range []string{"half typed", "sent"} {
		if press(e, "up"); e.ta.Value() != want {
			t.Errorf("up recalled %q, want %q", e.ta.Value(), want)
		}
	}

	// During a search it keeps the input, not the query.
	e.Clear()
	write(e, "before the search")
	press(e, "ctrl+r")
	write(e, "query")
	e.Discard()
	if press(e, "up"); e.ta.Value() != "before the search" {
		t.Errorf("up recalled %q", e.ta.Value())
	}
}

func TestSuggestion(t *testing.T) {
	e := newEditor()
	e.SetSuggestion("run the tests")
	for _, k := range []string{"tab", "right"} {
		e.Clear()
		e.SetSuggestion("run the tests")
		press(e, k)
		if got := e.ta.Value(); got != "run the tests" {
			t.Errorf("%s took %q", k, got)
		}
	}

	// A search hides the suggestion, so → only moves the cursor.
	e.Clear()
	e.SetHistory(NewHistory(filepath.Join(t.TempDir(), "history.jsonl"), "/project"))
	e.SetSuggestion("run the tests")
	press(e, "ctrl+r", "right")
	if got := e.ta.Value(); got != "" {
		t.Errorf("→ took %q during a search", got)
	}
	e.Dismiss()

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
