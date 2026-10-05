// Package editor is where the user writes: a multi-line input with history,
// slash-command completion, pasted text and images.
package editor

import (
	"image/color"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/ui/tui/imageinput"
	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// Input is what the user sent.
type Input struct {
	// Text has the paste references expanded.
	Text   string
	Images []litellm.Block
}

// Completion is a slash command the input may complete to.
type Completion struct {
	Name        string
	Aliases     []string
	Description string
	// Run sends the command on enter: it takes no arguments.
	Run bool
}

const (
	maxHeight = 10 // rows the input grows to before it scrolls
	maxMenu   = 8  // completions shown at once
)

const placeholder = "Ask anything · / for commands · ! for shell"

// Editor is the input. See the package documentation.
type Editor struct {
	ta       textarea.Model
	commands func() []Completion
	accent   color.Color

	history *History
	recall  int    // the history entry shown, -1 when none
	draft   string // what the input held before recalling

	pastes  pastes
	images  []litellm.Block
	loading int // pastes being read

	// offered are the commands listed when the input became a "/word",
	// which the menu matches it against; nil when it is not one.
	offered []Completion
	menu    []Completion
	menuAt  int
	menuOff bool // the user closed the menu for the current word

	suggestion string
}

// New returns an editor completing the slash commands listed by commands.
func New(commands func() []Completion) *Editor {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = maxHeight
	ta.MaxContentHeight = 1 << 20
	ta.Placeholder = placeholder
	ta.SetVirtualCursor(false)
	ta.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "❯ "
		}
		return "  "
	})
	km := textarea.DefaultKeyMap()
	km.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "shift+enter", "alt+enter"))
	km.Paste.SetEnabled(false)
	km.TransposeCharacterBackward.SetEnabled(false)
	ta.KeyMap = km
	ta.Focus()

	e := &Editor{ta: ta, commands: commands, recall: -1, accent: theme.Accent}
	e.restyle()
	return e
}

func (e *Editor) restyle() {
	s := textarea.DefaultStyles(theme.Dark)
	for _, st := range []*textarea.StyleState{&s.Focused, &s.Blurred} {
		st.Base = lipgloss.NewStyle()
		st.CursorLine = lipgloss.NewStyle()
		st.Text = lipgloss.NewStyle().Foreground(theme.Fg)
		st.Placeholder = lipgloss.NewStyle().Foreground(theme.Subtle)
		st.Prompt = lipgloss.NewStyle().Foreground(e.accent).Bold(true)
	}
	s.Cursor.Color = theme.Accent
	e.ta.SetStyles(s)
}

// SetAccent colors the prompt and the rules around the input.
func (e *Editor) SetAccent(c color.Color) {
	e.accent = c
	e.restyle()
}

// SetHistory sets what up and down recall.
func (e *Editor) SetHistory(h *History) {
	e.history, e.recall, e.draft = h, -1, ""
}

// SetSession starts on the conversation id, which what is sent from now on
// is recorded under.
func (e *Editor) SetSession(id string) {
	e.history.SetSession(id)
	e.recall, e.draft = -1, ""
}

// SetWidth sets the width the editor renders at.
func (e *Editor) SetWidth(w int) { e.ta.SetWidth(max(w, 10)) }

// SetSuggestion offers s as the next input: it shows while the input is
// empty, and tab or enter take it.
func (e *Editor) SetSuggestion(s string) {
	e.suggestion = s
	e.ta.Placeholder = placeholder
	if s != "" {
		e.ta.Placeholder = s + "  (tab)"
	}
}

// Empty reports whether the input holds nothing.
func (e *Editor) Empty() bool { return e.ta.Value() == "" && len(e.images) == 0 }

// Clear empties the input.
func (e *Editor) Clear() {
	e.ta.Reset()
	e.images = nil
	e.recall = -1
	e.offered, e.menu, e.menuOff = nil, nil, false
}

// Insert puts text in the input, before what it holds.
func (e *Editor) Insert(text string) {
	if v := e.ta.Value(); v != "" {
		text += "\n" + v
	}
	e.ta.SetValue(text)
	e.ta.MoveToEnd()
}

// Dismiss closes the completion menu; it reports whether one was open.
func (e *Editor) Dismiss() bool {
	if len(e.menu) == 0 {
		return false
	}
	e.menu, e.menuOff = nil, true
	return true
}

type imageMsg struct{ block litellm.Block }
type textMsg struct{ text string }
type pasteErrMsg struct{ err error }

// Update takes a message; in is set when the user sent the input.
func (e *Editor) Update(msg tea.Msg) (cmd tea.Cmd, in *Input) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return e.key(msg)
	case tea.PasteMsg:
		return e.paste(msg.Content), nil
	case imageMsg:
		e.loading--
		e.images = append(e.images, msg.block)
		return nil, nil
	case textMsg:
		e.loading--
		e.insertPaste(msg.text)
		return nil, nil
	case pasteErrMsg:
		e.loading--
		return func() tea.Msg { return Error{msg.err} }, nil
	}
	e.ta, cmd = e.ta.Update(msg)
	return cmd, nil
}

// Error is a paste that failed, for the TUI to report.
type Error struct{ Err error }

func (e *Editor) key(k tea.KeyPressMsg) (tea.Cmd, *Input) {
	switch k.String() {
	case "enter":
		if c, ok := e.selected(); ok {
			e.accept(c)
			if !c.Run {
				return nil, nil
			}
		}
		return nil, e.send()
	case "tab":
		if c, ok := e.selected(); ok {
			e.accept(c)
			return nil, nil
		}
		if e.suggestion != "" && e.ta.Value() == "" {
			e.ta.SetValue(e.suggestion)
			e.ta.MoveToEnd()
			e.SetSuggestion("")
		}
		return nil, nil
	case "up":
		if len(e.menu) > 0 {
			e.menuAt = (e.menuAt + len(e.menu) - 1) % len(e.menu)
			return nil, nil
		}
		if e.onFirstRow() && e.history != nil && e.recall+1 < e.history.Len() {
			if e.recall < 0 {
				e.draft = e.ta.Value()
			}
			e.recall++
			e.show(e.pastes.adopt(e.history.get(e.recall)))
			return nil, nil
		}
	case "down":
		if len(e.menu) > 0 {
			e.menuAt = (e.menuAt + 1) % len(e.menu)
			return nil, nil
		}
		if e.recall >= 0 && e.onLastRow() {
			e.recall--
			if e.recall < 0 {
				e.show(e.draft)
			} else {
				e.show(e.pastes.adopt(e.history.get(e.recall)))
			}
			return nil, nil
		}
	case "ctrl+v":
		e.loading++
		return readClipboard, nil
	case "backspace":
		if e.ta.Value() == "" && len(e.images) > 0 {
			e.images = e.images[:len(e.images)-1]
			return nil, nil
		}
		if n := refBefore(e.line(), e.ta.Column()); n > 0 {
			e.repeat(tea.KeyPressMsg{Code: tea.KeyBackspace}, n)
			return nil, nil
		}
	case "delete":
		if n := refAfter(e.line(), e.ta.Column()); n > 0 {
			e.repeat(tea.KeyPressMsg{Code: tea.KeyDelete}, n)
			return nil, nil
		}
	case "left":
		if n := refBefore(e.line(), e.ta.Column()); n > 0 {
			e.ta.SetCursorColumn(e.ta.Column() - n)
			return nil, nil
		}
	case "right":
		if n := refAfter(e.line(), e.ta.Column()); n > 0 {
			e.ta.SetCursorColumn(e.ta.Column() + n)
			return nil, nil
		}
	}

	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(k)
	e.refreshMenu()
	if e.suggestion != "" && e.ta.Value() != "" {
		e.SetSuggestion("")
	}
	return cmd, nil
}

// send empties the input and returns what it held; nil while a paste is
// still being read or when there is nothing to send.
func (e *Editor) send() *Input {
	if e.loading > 0 {
		return nil
	}
	text := strings.TrimSpace(e.ta.Value())
	if text == "" && len(e.images) == 0 {
		if e.suggestion == "" {
			return nil
		}
		text = e.suggestion
	}
	if e.history != nil && text != "" {
		e.history.Add(text, e.pastes.of(text))
	}
	in := &Input{Text: e.pastes.expand(text), Images: e.images}
	e.Clear()
	e.SetSuggestion("")
	return in
}

func (e *Editor) show(text string) {
	e.ta.SetValue(text)
	e.ta.MoveToEnd()
	e.menu = nil
}

// onFirstRow reports whether the cursor is on the input's first row, as
// wrapped; up there recalls history rather than moving.
func (e *Editor) onFirstRow() bool {
	return e.ta.Line() == 0 && e.ta.LineInfo().RowOffset == 0
}

func (e *Editor) onLastRow() bool {
	info := e.ta.LineInfo()
	return e.ta.Line() == e.ta.LineCount()-1 && info.RowOffset == info.Height-1
}

// line returns the runes of the line the cursor is on.
func (e *Editor) line() []rune {
	lines := strings.Split(e.ta.Value(), "\n")
	return []rune(lines[min(e.ta.Line(), len(lines)-1)])
}

func (e *Editor) repeat(k tea.KeyPressMsg, n int) {
	for range n {
		e.ta, _ = e.ta.Update(k)
	}
}

// refreshMenu completes a "/command" being typed as the input's first word.
// Listing the commands may look through the workspace for the skills that
// apply, so it happens once per "/", not per key.
func (e *Editor) refreshMenu() {
	word, ok := strings.CutPrefix(e.ta.Value(), "/")
	if !ok || strings.ContainsAny(word, " \n/") {
		e.offered, e.menu, e.menuOff = nil, nil, false
		return
	}
	if e.menuOff {
		return
	}
	if e.offered == nil {
		e.offered = append([]Completion{}, e.commands()...)
	}
	e.menu, e.menuAt = match(e.offered, word), 0
}

// match returns the completions whose name or alias matches q, best first:
// the whole name, then a prefix, then a part; an alias ranks below a name.
func match(cs []Completion, q string) []Completion {
	q = strings.ToLower(q)
	score := func(c Completion) int {
		best := 0
		for i, name := range append([]string{c.Name}, c.Aliases...) {
			s := 0
			switch {
			case q == "":
				s = 1
			case name == q:
				s = 4
			case strings.HasPrefix(name, q):
				s = 3
			case strings.Contains(name, q):
				s = 2
			}
			if i > 0 && s > 1 {
				s--
			}
			best = max(best, s)
		}
		return best
	}
	var out []Completion
	scores := map[string]int{}
	for _, c := range cs {
		if s := score(c); s > 0 {
			out = append(out, c)
			scores[c.Name] = s
		}
	}
	slices.SortStableFunc(out, func(a, b Completion) int { return scores[b.Name] - scores[a.Name] })
	return out
}

func (e *Editor) selected() (Completion, bool) {
	if len(e.menu) == 0 {
		return Completion{}, false
	}
	return e.menu[e.menuAt], true
}

func (e *Editor) accept(c Completion) {
	e.ta.SetValue("/" + c.Name + " ")
	e.ta.MoveToEnd()
	e.menu, e.menuOff = nil, true
}

func (e *Editor) paste(text string) tea.Cmd {
	if path := imageinput.ParseDroppedPath(text); path != "" {
		e.loading++
		return func() tea.Msg {
			block, err := imageinput.LoadFile(path)
			if err != nil {
				return pasteErrMsg{err}
			}
			return imageMsg{block}
		}
	}
	e.insertPaste(text)
	return nil
}

// newlines turns the line ends terminals paste, \r\n or a lone \r, into \n.
var newlines = strings.NewReplacer("\r\n", "\n", "\r", "\n")

func (e *Editor) insertPaste(text string) {
	text = newlines.Replace(text)
	if len([]rune(text)) > pasteInline {
		text = e.pastes.ref(text)
	}
	e.ta.InsertString(text)
	e.refreshMenu()
}

// readClipboard attaches the clipboard's image, or pastes its text when it
// holds none.
func readClipboard() tea.Msg {
	data, err := imageinput.ReadImage()
	if err != nil {
		return pasteErrMsg{err}
	}
	if data == nil {
		text, err := clipboard.ReadAll()
		if err != nil {
			return pasteErrMsg{err}
		}
		return textMsg{text}
	}
	block, err := imageinput.FromBytes(data)
	if err != nil {
		return pasteErrMsg{err}
	}
	return imageMsg{block}
}

// View renders the input between two rules, the images attached above it.
func (e *Editor) View(width int) string {
	rule := lipgloss.NewStyle().Foreground(e.accent).Render(strings.Repeat("─", width))
	lines := []string{rule}
	if len(e.images) > 0 {
		var chips []string
		for i := range e.images {
			chips = append(chips, theme.AccentText.Render("[image "+strconv.Itoa(i+1)+"]"))
		}
		lines = append(lines, ansi.Truncate(strings.Join(chips, " ")+theme.SubtleText.Render("  backspace removes"), width, "…"))
	}
	lines = append(lines, e.ta.View(), rule)
	return strings.Join(lines, "\n")
}

// Cursor is where the terminal's cursor goes, relative to the View.
func (e *Editor) Cursor() *tea.Cursor {
	c := e.ta.Cursor()
	if c == nil {
		return nil
	}
	c.Y++ // the rule
	if len(e.images) > 0 {
		c.Y++
	}
	return c
}

// Menu renders the completions, empty when none show.
func (e *Editor) Menu(width int) []string {
	if len(e.menu) == 0 {
		return nil
	}
	from, to := 0, len(e.menu)
	if to > maxMenu {
		from = min(max(e.menuAt-maxMenu/2, 0), len(e.menu)-maxMenu)
		to = from + maxMenu
	}
	nameW := 0
	for _, c := range e.menu[from:to] {
		nameW = max(nameW, ansi.StringWidth(c.Name)+1)
	}
	var out []string
	for i, c := range e.menu[from:to] {
		name := "/" + c.Name + strings.Repeat(" ", nameW-ansi.StringWidth(c.Name))
		line := "  " + theme.Text.Render(name) + "  " + theme.SubtleText.Render(c.Description)
		if from+i == e.menuAt {
			line = theme.Selected.Render("❯ "+name) + "  " + theme.MutedText.Render(c.Description)
		}
		out = append(out, ansi.Truncate(line, width, "…"))
	}
	return out
}
