package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

// Pastes past this length are held out of the textarea behind a reference —
// 50KB of log in the editor makes it unusable. The reference expands back at
// submit, so this is an editing convenience, not context management.
const pasteInlineLimit = 1000

// pasteUnavailable replaces a recalled reference whose body history dropped.
// Outside the pasteRef pattern on purpose, so expansion ignores it.
const pasteUnavailable = "[Pasted text unavailable]"

var (
	pasteRef        = regexp.MustCompile(`\[Pasted text #(\d+)(?: \+\d+ lines)?\]`)
	pasteRefAtEnd   = regexp.MustCompile(`\[Pasted text #\d+(?: \+\d+ lines)?\]$`)
	pasteRefAtStart = regexp.MustCompile(`^\[Pasted text #\d+(?: \+\d+ lines)?\]`)
)

// pasteTextReadyMsg carries clipboard text back to the update loop. Reading
// the clipboard blocks, so it happens off the loop like the image path does.
type pasteTextReadyMsg struct{ Text string }

func readClipboardText() tea.Msg {
	text, _ := clipboard.ReadAll()
	return pasteTextReadyMsg{Text: text}
}

func formatPasteRef(id int, text string) string {
	if lines := strings.Count(text, "\n"); lines > 0 {
		return fmt.Sprintf("[Pasted text #%d +%d lines]", id, lines+1)
	}
	return fmt.Sprintf("[Pasted text #%d]", id)
}

// insertPaste puts text into the input, holding the body back behind a
// reference once it is long enough to swamp the editor.
func (m *Model) insertPaste(text string) {
	if text == "" {
		return
	}
	if len([]rune(text)) <= pasteInlineLimit {
		m.Input.InsertString(text)
		return
	}
	if m.Pasted == nil {
		m.Pasted = make(map[int]string)
	}
	m.nextPasteID++
	m.Pasted[m.nextPasteID] = text
	m.Input.InsertString(formatPasteRef(m.nextPasteID, text))
}

// expandPasteRefs restores held-back bodies before submission. Ids we never
// issued are left alone — that is literal text someone typed, not a reference.
func (m *Model) expandPasteRefs(text string) string {
	if len(m.Pasted) == 0 {
		return text
	}
	return pasteRef.ReplaceAllStringFunc(text, func(ref string) string {
		id, err := strconv.Atoi(pasteRef.FindStringSubmatch(ref)[1])
		if err != nil {
			return ref
		}
		if body, ok := m.Pasted[id]; ok {
			return body
		}
		return ref
	})
}

// pastedFor returns the bodies text references, so an entry stores what it
// needs to expand and nothing else.
func (m *Model) pastedFor(text string) map[int]string {
	if len(m.Pasted) == 0 {
		return nil
	}
	var out map[int]string
	for _, ref := range pasteRef.FindAllStringSubmatch(text, -1) {
		id, err := strconv.Atoi(ref[1])
		if err != nil {
			continue
		}
		body, ok := m.Pasted[id]
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[int]string)
		}
		out[id] = body
	}
	return out
}

// adoptPasted re-issues a recalled entry's bodies under this session's ids.
//
// Renumbering is the point: ids restart at 1 every launch, so a stored "#1"
// would otherwise expand to whatever this session pasted first — one body
// silently swapped for another. The three cases are distinct: a body we kept
// is re-issued, one history dropped becomes visible, and an id we never stored
// is text somebody typed and stays put.
func (m *Model) adoptPasted(text string, bodies map[int]string) string {
	if len(bodies) == 0 {
		return text
	}
	return pasteRef.ReplaceAllStringFunc(text, func(ref string) string {
		id, err := strconv.Atoi(pasteRef.FindStringSubmatch(ref)[1])
		if err != nil {
			return ref
		}
		body, ok := bodies[id]
		switch {
		case !ok:
			return ref
		case body == "":
			return pasteUnavailable
		default:
			return formatPasteRef(m.adoptBody(body), body)
		}
	})
}

// recallHistory returns the entry at index, ready for this session.
func (m *Model) recallHistory(index int) string {
	return m.adoptPasted(m.history.Get(index), m.history.Pasted(index))
}

// adoptBody returns the id holding body here, issuing one if it is new.
// Reusing keeps repeated recalls of the same entry from piling up ids.
func (m *Model) adoptBody(body string) int {
	for id, held := range m.Pasted {
		if held == body {
			return id
		}
	}
	if m.Pasted == nil {
		m.Pasted = make(map[int]string)
	}
	m.nextPasteID++
	m.Pasted[m.nextPasteID] = body
	return m.nextPasteID
}

// handlePasteRefKey makes a reference behave as one character under the keys
// that cross or delete it.
//
// Movement matters as much as deletion: a cursor resting inside a reference
// lets one backspace break it into "[Pasted text #]", after which expansion no
// longer matches and the body is silently sent as literal text. Ctrl/alt
// variants carry their own KeyType and keep the textarea's word behaviour.
func (m *Model) handlePasteRefKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if len(m.Pasted) == 0 || msg.Alt {
		return m, nil, false
	}
	col := cursorColumn(m)
	value, row := m.Input.Value(), m.Input.Line()

	switch msg.Type {
	case tea.KeyBackspace:
		if n := pasteRefRunesBefore(value, row, col); n > 0 {
			return m, nil, m.eraseRef(n, tea.KeyBackspace)
		}
	case tea.KeyDelete:
		if n := pasteRefRunesAfter(value, row, col); n > 0 {
			return m, nil, m.eraseRef(n, tea.KeyDelete)
		}
	case tea.KeyLeft:
		if n := pasteRefRunesBefore(value, row, col); n > 0 {
			m.Input.SetCursor(col - n)
			return m, nil, true
		}
	case tea.KeyRight:
		if n := pasteRefRunesAfter(value, row, col); n > 0 {
			m.Input.SetCursor(col + n)
			return m, nil, true
		}
	}
	return m, nil, false
}

// eraseRef replays n key presses into the textarea rather than rewriting its
// value: it owns cursor position and soft-wrap state, and SetValue would
// strand the cursor at the end of the buffer.
func (m *Model) eraseRef(n int, key tea.KeyType) bool {
	for range n {
		m.Input, _ = m.Input.Update(tea.KeyMsg{Type: key})
	}
	m.adjustInputHeight()
	return true
}

// cursorColumn returns the cursor's offset within its logical line. LineInfo
// reports position within a soft-wrapped display row, so the row's start
// column has to be added back to get there.
func cursorColumn(m *Model) int {
	li := m.Input.LineInfo()
	return li.StartColumn + li.ColumnOffset
}

// pasteRefRunesBefore reports the length in runes of a reference ending exactly
// at the cursor, or 0 when there is none.
func pasteRefRunesBefore(value string, row, col int) int {
	runes, ok := lineRunes(value, row, col)
	if !ok || col == 0 {
		return 0
	}
	before := string(runes[:col])
	loc := pasteRefAtEnd.FindStringIndex(before)
	if loc == nil {
		return 0
	}
	return len([]rune(before[loc[0]:]))
}

// pasteRefRunesAfter reports the length in runes of a reference starting
// exactly at the cursor, or 0 when there is none.
func pasteRefRunesAfter(value string, row, col int) int {
	runes, ok := lineRunes(value, row, col)
	if !ok || col == len(runes) {
		return 0
	}
	match := pasteRefAtStart.FindString(string(runes[col:]))
	return len([]rune(match))
}

func lineRunes(value string, row, col int) ([]rune, bool) {
	lines := strings.Split(value, "\n")
	if row < 0 || row >= len(lines) {
		return nil, false
	}
	runes := []rune(lines[row])
	if col < 0 || col > len(runes) {
		return nil, false
	}
	return runes, true
}
