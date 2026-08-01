package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/storage"
)

func pasteTestModel(t *testing.T) *Model {
	t.Helper()
	m := New(nil, "test-model")
	m.Ready = true
	m.Input.SetWidth(80)
	return m
}

func backspace(m *Model) *Model {
	next, _, handled := m.handlePasteRefKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if !handled {
		// Not a reference boundary — let the textarea take it, as the real
		// dispatch chain would.
		m.Input, _ = m.Input.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		return m
	}
	return next.(*Model)
}

// Short pastes go in verbatim: swapping them for a reference would hide text
// the user can perfectly well read and edit in place.
func TestShortPasteStaysInline(t *testing.T) {
	m := pasteTestModel(t)
	m.insertPaste("just a normal line")

	if got := m.Input.Value(); got != "just a normal line" {
		t.Fatalf("input = %q, want the text inline", got)
	}
	if len(m.Pasted) != 0 {
		t.Fatalf("held back %d bodies for a short paste", len(m.Pasted))
	}
}

// Past the limit the editor shows one token, and the body comes back at submit
// — the model must see exactly what was pasted.
func TestLargePasteBecomesReferenceAndExpands(t *testing.T) {
	m := pasteTestModel(t)
	body := strings.Repeat("log line\n", 400)

	m.Input.InsertString("look at this ")
	m.insertPaste(body)

	shown := m.Input.Value()
	if strings.Contains(shown, "log line") {
		t.Fatalf("body leaked into the editor: %.80s", shown)
	}
	if !strings.Contains(shown, "[Pasted text #1 +401 lines]") {
		t.Fatalf("no reference in %q", shown)
	}

	expanded := m.expandPasteRefs(shown)
	if expanded != "look at this "+body {
		t.Fatal("expansion did not restore the exact body")
	}
}

// One keystroke, whole token. Erasing 27 characters individually is what makes
// people give up on the feature.
func TestBackspaceRemovesWholeReference(t *testing.T) {
	m := pasteTestModel(t)
	m.Input.InsertString("before ")
	m.insertPaste(strings.Repeat("x", pasteInlineLimit+1))

	m = backspace(m)

	if got := m.Input.Value(); got != "before " {
		t.Fatalf("input = %q, want the reference gone in one press", got)
	}
}

// The atomic delete must only fire at a reference boundary; ordinary text next
// to one still erases a character at a time.
func TestBackspaceOutsideReferenceIsNormal(t *testing.T) {
	m := pasteTestModel(t)
	m.insertPaste(strings.Repeat("x", pasteInlineLimit+1))
	m.Input.InsertString("tail")

	m = backspace(m)

	got := m.Input.Value()
	if !strings.HasSuffix(got, "tai") {
		t.Fatalf("input = %q, want a single character removed", got)
	}
	if !strings.Contains(got, "[Pasted text #1]") {
		t.Fatalf("reference was destroyed by an unrelated backspace: %q", got)
	}
}

func pressKey(t *testing.T, m *Model, k tea.KeyType) *Model {
	t.Helper()
	next, _, handled := m.handlePasteRefKey(tea.KeyMsg{Type: k})
	if !handled {
		m.Input, _ = m.Input.Update(tea.KeyMsg{Type: k})
		return m
	}
	return next.(*Model)
}

// One press crosses the whole token, in both directions.
func TestArrowKeysStepOverReference(t *testing.T) {
	m := pasteTestModel(t)
	m.Input.InsertString("a ")
	m.insertPaste(strings.Repeat("w", pasteInlineLimit+1))
	m.Input.InsertString("b")

	refLen := len([]rune("[Pasted text #1]"))
	end := len([]rune(m.Input.Value()))

	m.Input.SetCursor(end - 1) // just after the reference, before "b"
	m = pressKey(t, m, tea.KeyLeft)
	if got := cursorColumn(m); got != end-1-refLen {
		t.Fatalf("left landed at %d, want %d (before the reference)", got, end-1-refLen)
	}

	m = pressKey(t, m, tea.KeyRight)
	if got := cursorColumn(m); got != end-1 {
		t.Fatalf("right landed at %d, want %d (after the reference)", got, end-1)
	}
}

// The point of atomic movement: the cursor never rests inside the token, so no
// single keystroke can break it into "[Pasted text #]" and strand the body.
func TestCursorCannotLandInsideReference(t *testing.T) {
	m := pasteTestModel(t)
	m.insertPaste(strings.Repeat("v", pasteInlineLimit+1))
	shown := m.Input.Value()

	m.Input.CursorEnd()
	m = pressKey(t, m, tea.KeyLeft)
	m = pressKey(t, m, tea.KeyBackspace)

	got := m.Input.Value()
	if got != "" && got != shown {
		t.Fatalf("input = %q — the reference was left half-erased", got)
	}
}

// Forward delete has to be atomic too, or approaching from the left breaks it.
func TestDeleteRemovesWholeReference(t *testing.T) {
	m := pasteTestModel(t)
	m.insertPaste(strings.Repeat("u", pasteInlineLimit+1))
	m.Input.InsertString("after")
	m.Input.SetCursor(0)

	m = pressKey(t, m, tea.KeyDelete)

	if got := m.Input.Value(); got != "after" {
		t.Fatalf("input = %q, want the reference gone in one press", got)
	}
}

// Most terminals deliver a paste as a bracketed-paste event, not Ctrl+V.
// Falling through to the textarea inserts the body verbatim and the reference
// never forms — the feature would be dead outside Ctrl+V.
func TestTerminalPasteGoesThroughReference(t *testing.T) {
	m := pasteTestModel(t)
	body := strings.Repeat("z", pasteInlineLimit+1)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(body), Paste: true})
	m = mustModel(t, next)

	shown := m.Input.Value()
	if shown != "[Pasted text #1]" {
		t.Fatalf("input = %.60q, want a reference", shown)
	}
	if m.expandPasteRefs(shown) != body {
		t.Fatal("expansion did not restore the pasted body")
	}
}

// Dropping an image path is also a paste event and must still reach OnDrop.
func TestTerminalPasteStillReachesDropHandler(t *testing.T) {
	m := pasteTestModel(t)
	called := ""
	m.config.OnDrop = func(_ *Model, text string) tea.Cmd {
		called = text
		return func() tea.Msg { return nil }
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/tmp/shot.png"), Paste: true})

	if called != "/tmp/shot.png" {
		t.Fatalf("OnDrop got %q", called)
	}
	if got := mustModel(t, next).Input.Value(); got != "" {
		t.Fatalf("input = %q, want the drop handled without inserting text", got)
	}
}

func historyModel(t *testing.T, path string) *Model {
	t.Helper()
	m := New(nil, "test-model", Config{History: storage.NewHistory(path, "proj", "sess")})
	m.Ready = true
	m.Input.SetWidth(80)
	return m
}

// Ids restart at 1 each launch, so a stored "#1" recalled into a session that
// already pasted something would expand to *that* body — one paste silently
// swapped for another.
func TestRecalledPasteDoesNotStealAnotherSessionsBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")

	// Session one submits a prompt holding a reference.
	first := historyModel(t, path)
	firstBody := strings.Repeat("first\n", 400)
	first.insertPaste(firstBody)
	if _, ok := first.prepareSubmission(); !ok {
		t.Fatal("submission rejected")
	}

	// Session two pastes something of its own, taking id 1.
	second := historyModel(t, path)
	secondBody := strings.Repeat("second\n", 400)
	second.insertPaste(secondBody)
	second.Input.Reset()

	recalled := second.recallHistory(0)
	expanded := second.expandPasteRefs(recalled)

	if strings.Contains(expanded, "second") {
		t.Fatalf("recall expanded to this session's paste: %.60q", expanded)
	}
	if expanded != firstBody {
		t.Fatalf("recall did not restore the stored body: %.60q", expanded)
	}
}

// Recalling the same entry repeatedly must not issue a fresh id per press.
func TestRepeatedRecallReusesTheSameBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")

	first := historyModel(t, path)
	first.insertPaste(strings.Repeat("body\n", 400))
	if _, ok := first.prepareSubmission(); !ok {
		t.Fatal("submission rejected")
	}

	second := historyModel(t, path)
	for range 3 {
		second.recallHistory(0)
	}
	if len(second.Pasted) != 1 {
		t.Fatalf("held %d bodies after three recalls, want 1", len(second.Pasted))
	}
}

// A dropped body must not leave a live-looking reference: submitting it sends
// "[Pasted text #1 +N lines]" to the model with nothing warning the user.
func TestRecalledPasteShowsWhenTheBodyIsGone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")

	first := historyModel(t, path)
	first.insertPaste(strings.Repeat("x", storage.MaxPastedBytes+1))
	if _, ok := first.prepareSubmission(); !ok {
		t.Fatal("submission rejected")
	}

	recalled := historyModel(t, path).recallHistory(0)

	if strings.Contains(recalled, "[Pasted text #") {
		t.Fatalf("recall = %q, want no reference to a body that is gone", recalled)
	}
	if !strings.Contains(recalled, pasteUnavailable) {
		t.Fatalf("recall = %q, want the loss shown", recalled)
	}
}

// Recall must not rewrite a hand-typed reference, even beside a real one.
func TestRecallLeavesHandTypedReferencesAlone(t *testing.T) {
	m := pasteTestModel(t)

	got := m.adoptPasted("real [Pasted text #1] typed [Pasted text #9]", map[int]string{1: "body"})

	if !strings.Contains(got, "[Pasted text #9]") {
		t.Fatalf("recall = %q, want the hand-typed reference untouched", got)
	}
	if strings.Contains(got, pasteUnavailable) {
		t.Fatalf("recall = %q, want no loss reported for text nobody pasted", got)
	}
}

// Ids we never issued are literal text somebody typed, not a reference.
func TestUnknownReferenceIsLeftAlone(t *testing.T) {
	m := pasteTestModel(t)
	m.insertPaste(strings.Repeat("y", pasteInlineLimit+1))

	const typed = "see [Pasted text #99] please"
	if got := m.expandPasteRefs(typed); got != typed {
		t.Fatalf("expanded an id we never issued: %q", got)
	}
}

// The handler only helps if the dispatch chain actually reaches it — an
// earlier handler claiming backspace would leave the unit tests passing and
// the feature dead.
func TestBackspaceReachesHandlerThroughDispatch(t *testing.T) {
	m := pasteTestModel(t)
	m.Input.Focus()
	m.Input.InsertString("keep ")
	m.insertPaste(strings.Repeat("q", pasteInlineLimit+1))

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})

	if got := mustModel(t, next).Input.Value(); got != "keep " {
		t.Fatalf("input = %q, want the reference removed via dispatch", got)
	}
}

// The paste stays counted until the text lands. Releasing the count early
// lets a submit race the clipboard read and send a half-populated input.
func TestPasteCountClearsOnlyWhenTextArrives(t *testing.T) {
	m := pasteTestModel(t)
	m.Pasting = 1

	next, cmd := m.Update(PasteTextMsg{})
	m = mustModel(t, next)
	if m.Pasting != 1 {
		t.Fatalf("Pasting = %d before the text arrived, want 1", m.Pasting)
	}
	if cmd == nil {
		t.Fatal("no clipboard read scheduled")
	}

	next, _ = m.Update(pasteTextReadyMsg{Text: "hello"})
	m = mustModel(t, next)
	if m.Pasting != 0 {
		t.Fatalf("Pasting = %d after the text landed, want 0", m.Pasting)
	}
	if got := m.Input.Value(); got != "hello" {
		t.Fatalf("input = %q, want the pasted text", got)
	}
}

// Inserting bypasses the textarea's key path, so the height has to be
// recomputed explicitly — otherwise a multi-line paste stays a one-line box.
func TestInlinePasteGrowsTheInput(t *testing.T) {
	m := pasteTestModel(t)
	m.Pasting = 1

	next, _ := m.Update(pasteTextReadyMsg{Text: "one\ntwo\nthree"})

	if got := mustModel(t, next).Input.Height(); got < 3 {
		t.Fatalf("input height = %d, want at least the 3 pasted lines", got)
	}
}

// A reference in the middle of a line must be found by the cursor position,
// not by scanning the whole buffer.
func TestBackspaceFindsReferenceOnLaterLine(t *testing.T) {
	m := pasteTestModel(t)
	m.Input.InsertString("first line\nsecond ")
	m.insertPaste(strings.Repeat("z", pasteInlineLimit+1))

	m = backspace(m)

	if got := m.Input.Value(); got != "first line\nsecond " {
		t.Fatalf("input = %q, want only the reference removed", got)
	}
}
