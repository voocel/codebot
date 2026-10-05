// Package editor is where the user writes: a multi-line input with history
// and its search, completion of slash commands and of files mentioned with
// @, pasted text and images, and the user's own editor a key away.
package editor

import (
	"image/color"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"unicode"

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

const placeholder = "Ask anything · / for commands · @ for files · ! for shell"

// Editor is the input. See the package documentation.
type Editor struct {
	ta       textarea.Model
	commands func() []Completion
	root     func() string
	accent   color.Color

	history *History
	recall  int    // the history entry shown, -1 when none
	draft   string // what the input held before recalling or searching

	pastes  pastes
	images  []litellm.Block
	loading int // pastes being read

	// The menu offers what the word at the cursor completes to: a command
	// for the "/word" the input starts with, a file for an "@path". sign is
	// the word's, 0 for none; the commands and files are listed as it
	// begins, and the menu matches it against them. While the user
	// searches the history, the menu offers the entries holding what they
	// type instead.
	sign      rune
	cmds      []Completion
	files     []string
	searching bool
	menu      []item
	menuAt    int
	menuOff   bool // the user closed the menu on the word

	suggestion string
}

// item is an entry of the menu.
type item struct {
	label, note string
	pick        func() // puts it in the input
	run         bool   // enter sends the input once it is picked
}

// New returns an editor completing the slash commands listed by commands
// and the files under the directory root returns.
func New(commands func() []Completion, root func() string) *Editor {
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

	e := &Editor{ta: ta, commands: commands, root: root, recall: -1, accent: theme.Accent}
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
	e.placehold()
}

func (e *Editor) placehold() {
	switch {
	case e.searching:
		e.ta.Placeholder = "Type to search what you sent before"
	case e.suggestion != "":
		e.ta.Placeholder = e.suggestion + "  (tab)"
	default:
		e.ta.Placeholder = placeholder
	}
}

// Empty reports whether the input holds nothing; a search of the history
// is something.
func (e *Editor) Empty() bool { return e.ta.Value() == "" && len(e.images) == 0 && !e.searching }

// Clear empties the input.
func (e *Editor) Clear() {
	e.ta.Reset()
	e.images = nil
	e.recall = -1
	e.sign, e.menu, e.menuOff = 0, nil, false
	e.endSearch()
}

// Insert puts text in the input, before what it holds.
func (e *Editor) Insert(text string) {
	if v := e.ta.Value(); v != "" {
		text += "\n" + v
	}
	e.ta.SetValue(text)
	e.ta.MoveToEnd()
}

// Dismiss closes the menu, and ends a search of the history with the input
// as it was; it reports whether there was one.
func (e *Editor) Dismiss() bool {
	switch {
	case e.searching:
		e.endSearch()
		e.show(e.draft)
	case len(e.menu) > 0:
		e.menu, e.menuOff = nil, true
	default:
		return false
	}
	return true
}

type imageMsg struct{ block litellm.Block }
type textMsg struct{ text string }
type pasteErrMsg struct{ err error }
type editedMsg struct{ text string }

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
		return e.insertPaste(msg.text), nil
	case pasteErrMsg:
		e.loading--
		return report(msg.err), nil
	case filesMsg:
		e.files = msg.files
		return e.refreshMenu(), nil
	case editedMsg:
		e.show(msg.text)
		return e.edited(), nil
	}
	e.ta, cmd = e.ta.Update(msg)
	return cmd, nil
}

// Error is a paste or an edit that failed, for the TUI to report.
type Error struct{ Err error }

func report(err error) tea.Cmd { return func() tea.Msg { return Error{err} } }

func (e *Editor) key(k tea.KeyPressMsg) (tea.Cmd, *Input) {
	switch k.String() {
	case "enter":
		if it, ok := e.selected(); ok {
			cmd := e.pick(it)
			if !it.run {
				return cmd, nil
			}
		}
		if e.searching {
			return nil, nil
		}
		return nil, e.send()
	case "tab":
		if it, ok := e.selected(); ok {
			return e.pick(it), nil
		}
		if e.suggestion != "" && e.ta.Value() == "" {
			e.ta.SetValue(e.suggestion)
			e.ta.MoveToEnd()
			e.SetSuggestion("")
		}
		return nil, nil
	case "up":
		if len(e.menu) > 0 || e.searching {
			e.move(-1)
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
		if len(e.menu) > 0 || e.searching {
			e.move(1)
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
	case "ctrl+r":
		if e.searching {
			e.move(1)
			return nil, nil
		}
		if e.history != nil {
			e.search()
		}
		return nil, nil
	case "ctrl+g":
		return e.external(), nil
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
	return tea.Batch(cmd, e.edited()), nil
}

// edited follows a change of the input.
func (e *Editor) edited() tea.Cmd {
	if e.suggestion != "" && e.ta.Value() != "" {
		e.SetSuggestion("")
	}
	return e.refreshMenu()
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

// search starts a search of the history, keeping the input to come back to.
func (e *Editor) search() {
	e.draft, e.recall = e.ta.Value(), -1
	e.searching = true
	e.ta.Reset()
	e.placehold()
	e.refreshMenu()
}

func (e *Editor) endSearch() {
	e.searching = false
	e.placehold()
}

// external opens the input in the user's editor, to come back with what
// they save.
func (e *Editor) external() tea.Cmd {
	f, err := os.CreateTemp("", "codebot-prompt-*.md")
	if err != nil {
		return report(err)
	}
	_, err = f.WriteString(e.ta.Value())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return report(err)
	}
	return Open(f.Name(), func(err error) tea.Msg {
		defer os.Remove(f.Name())
		if err != nil {
			return Error{err}
		}
		data, err := os.ReadFile(f.Name())
		if err != nil {
			return Error{err}
		}
		return editedMsg{strings.TrimRight(string(data), "\n")}
	})
}

// Open opens path in the user's editor, $VISUAL or $EDITOR, else vi, and
// returns done's message once it exits.
func Open(path string, done func(err error) tea.Msg) tea.Cmd {
	name := os.Getenv("VISUAL")
	if name == "" {
		name = os.Getenv("EDITOR")
	}
	if name == "" {
		name = "vi"
	}
	// It may come with flags, "code --wait".
	args := append(strings.Fields(name), path)
	return tea.ExecProcess(exec.Command(args[0], args[1:]...), done)
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

// refreshMenu follows the input with the menu. Listing the commands may
// look through the workspace for the skills that apply, and listing the
// files reads it, so either happens once per word, not per key; the files
// are listed off the TUI's goroutine, and meanwhile the menu matches those
// listed last.
func (e *Editor) refreshMenu() tea.Cmd {
	if e.searching {
		e.setMenu(e.historyItems(e.ta.Value()))
		return nil
	}
	sign, q := e.word()
	var cmd tea.Cmd
	if sign != e.sign {
		e.sign, e.menuOff = sign, false
		switch sign {
		case '/':
			e.cmds = e.commands()
		case '@':
			root := e.root()
			cmd = func() tea.Msg { return filesMsg{listFiles(root)} }
		}
	}
	switch {
	case sign == 0 || e.menuOff:
		e.menu = nil
	case sign == '/':
		e.setMenu(e.commandItems(q))
	default:
		e.setMenu(e.fileItems(q))
	}
	return cmd
}

// word returns the word at the cursor the menu completes, after its sign:
// a "/command" the input starts with, or an "@path" starting a word.
func (e *Editor) word() (sign rune, q string) {
	if w, ok := strings.CutPrefix(e.ta.Value(), "/"); ok && !strings.ContainsAny(w, " \n/") {
		return '/', w
	}
	line := e.line()
	col := min(e.ta.Column(), len(line))
	start := col
	for start > 0 && !unicode.IsSpace(line[start-1]) {
		start--
	}
	if start < col && line[start] == '@' {
		return '@', string(line[start+1 : col])
	}
	return 0, ""
}

func (e *Editor) setMenu(items []item) { e.menu, e.menuAt = items, 0 }

func (e *Editor) move(by int) {
	if n := len(e.menu); n > 0 {
		e.menuAt = (e.menuAt + by + n) % n
	}
}

func (e *Editor) selected() (item, bool) {
	if len(e.menu) == 0 {
		return item{}, false
	}
	return e.menu[e.menuAt], true
}

// pick puts it in the input, then follows the input: a directory picked
// goes on to what it holds.
func (e *Editor) pick(it item) tea.Cmd {
	it.pick()
	return e.edited()
}

func (e *Editor) commandItems(q string) []item {
	var items []item
	for _, c := range match(e.cmds, q) {
		items = append(items, item{label: "/" + c.Name, note: c.Description, run: c.Run, pick: func() {
			e.ta.SetValue("/" + c.Name + " ")
			e.ta.MoveToEnd()
		}})
	}
	return items
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

func (e *Editor) fileItems(q string) []item {
	var items []item
	for _, p := range rank(e.files, q) {
		items = append(items, item{label: p, pick: func() { e.mention(p) }})
	}
	return items
}

// mention puts "@path" in place of the "@word" at the cursor: quoted when
// it holds a space, and open for more when it is a directory's.
func (e *Editor) mention(path string) {
	_, q := e.word()
	e.repeat(tea.KeyPressMsg{Code: tea.KeyBackspace}, len([]rune(q))+1)
	if strings.Contains(path, " ") {
		path = strconv.Quote(path)
	}
	if !strings.HasSuffix(path, "/") {
		path += " "
	}
	e.ta.InsertString("@" + path)
}

// historyItems lists the entries of the history that hold q, newest first.
func (e *Editor) historyItems(q string) []item {
	q = strings.ToLower(q)
	var items []item
	for i := range e.history.Len() {
		en := e.history.get(i)
		if !strings.Contains(strings.ToLower(en.text), q) {
			continue
		}
		items = append(items, item{label: strings.Join(strings.Fields(en.text), " "), pick: func() {
			e.endSearch()
			e.show(e.pastes.adopt(en))
		}})
	}
	return items
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
	return e.insertPaste(text)
}

// newlines turns the line ends terminals paste, \r\n or a lone \r, into \n.
var newlines = strings.NewReplacer("\r\n", "\n", "\r", "\n")

func (e *Editor) insertPaste(text string) tea.Cmd {
	text = newlines.Replace(text)
	if len([]rune(text)) > pasteInline {
		text = e.pastes.ref(text)
	}
	e.ta.InsertString(text)
	return e.edited()
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
// While the user searches the history, the rule above says so.
func (e *Editor) View(width int) string {
	ink := lipgloss.NewStyle().Foreground(e.accent)
	rule := ink.Render(strings.Repeat("─", width))
	lines := []string{rule}
	if e.searching {
		const title = "── Search history "
		lines[0] = ink.Render("── ") + theme.Selected.Render("Search history") + " " + ink.Render(strings.Repeat("─", max(width-ansi.StringWidth(title), 0)))
	}
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

// Menu renders the menu, empty when none shows.
func (e *Editor) Menu(width int) []string {
	if len(e.menu) == 0 {
		if e.searching && e.ta.Value() != "" {
			return []string{"  " + theme.SubtleText.Render("Nothing sent before holds this")}
		}
		return nil
	}
	from, to := 0, len(e.menu)
	if to > maxMenu {
		from = min(max(e.menuAt-maxMenu/2, 0), len(e.menu)-maxMenu)
		to = from + maxMenu
	}
	labelW := 0
	for _, it := range e.menu[from:to] {
		labelW = max(labelW, ansi.StringWidth(it.label))
	}
	var out []string
	for i, it := range e.menu[from:to] {
		label := it.label + strings.Repeat(" ", labelW-ansi.StringWidth(it.label))
		line := "  " + theme.Text.Render(label) + "  " + theme.SubtleText.Render(it.note)
		if from+i == e.menuAt {
			line = theme.Selected.Render("❯ "+label) + "  " + theme.MutedText.Render(it.note)
		}
		out = append(out, ansi.Truncate(line, width, "…"))
	}
	return out
}
