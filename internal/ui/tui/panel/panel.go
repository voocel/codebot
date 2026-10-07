// Package panel holds the views that replace the editor at the bottom of the
// screen until dismissed: permission requests, questions, lists and text.
package panel

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/ui/tui/theme"
)

type Panel interface {
	// Update receives keys and any message the TUI doesn't handle itself.
	Update(msg tea.Msg) (cmd tea.Cmd, done bool)
	View(width, height int) string
}

type Initer interface {
	Init() tea.Cmd
}

// A Request answers the agent and may be withdrawn by Key. Requests wait
// their turn; Queue tells the shown one how many wait behind it.
type Request interface {
	Panel
	Key() any
	Queue(behind int)
}

type queue struct{ behind int }

func (q *queue) Queue(behind int) { q.behind = behind }

func (q queue) title(t string) string {
	if q.behind > 0 {
		return t + " · " + strconv.Itoa(q.behind) + " more"
	}
	return t
}

// In a menu, ↑↓ move the cursor and enter or an option's number picks.
type menu struct{ n, cursor int }

// key returns the picked option, or -1.
func (m *menu) key(k string) int {
	switch k {
	case "up", "k":
		m.cursor = (m.cursor + m.n - 1) % m.n
	case "down", "j":
		m.cursor = (m.cursor + 1) % m.n
	case "enter":
		return m.cursor
	default:
		if i, err := strconv.Atoi(k); err == nil && i >= 1 && i <= m.n {
			m.cursor = i - 1
			return m.cursor
		}
	}
	return -1
}

func (m menu) numbered(i int, text string) string {
	return row(strconv.Itoa(i+1)+". "+text, i == m.cursor)
}

// field is a free-form answer line offered in place of an option.
type field struct {
	input textinput.Model
	on    bool
}

func newField(placeholder string) field {
	return field{input: NewInput(placeholder)}
}

// NewInput returns a one-line input without a prompt, styled as the editor
// is: the text in the body color, and the placeholder subtle, fainter than a
// label beside it.
func NewInput(placeholder string) textinput.Model {
	s := textinput.DefaultStyles(theme.Dark)
	for _, st := range []*textinput.StyleState{&s.Focused, &s.Blurred} {
		st.Text = theme.Text
		st.Placeholder = theme.SubtleText
	}
	s.Cursor.Color = theme.Accent
	in := textinput.New()
	in.SetStyles(s)
	in.Prompt = ""
	in.Placeholder = placeholder
	return in
}

func (f *field) open(value string) tea.Cmd {
	f.on = true
	f.input.SetValue(value)
	f.input.CursorEnd()
	return f.input.Focus()
}

// update reports the text when enter closes the field; esc only closes it.
func (f *field) update(msg tea.Msg) (cmd tea.Cmd, text string, entered bool) {
	if k, ok := key(msg); ok && (k == "enter" || k == "esc") {
		f.on = false
		f.input.Blur()
		return nil, strings.TrimSpace(f.input.Value()), k == "enter"
	}
	f.input, cmd = f.input.Update(msg)
	return cmd, "", false
}

func (f *field) view(width int) string {
	f.input.SetWidth(max(width-5, 10))
	return theme.Selected.Render("❯ ") + f.input.View()
}

// head is the text above a request's options. What doesn't fit is cut, and
// pgup/pgdown scroll it.
type head struct{ top, room int }

func (h *head) key(k string) bool {
	switch k {
	case "pgup":
		h.top = max(h.top-h.room, 0)
	case "pgdown":
		h.top += h.room
	default:
		return false
	}
	return true
}

// follow scrolls the layout from the last fit so that line is visible.
func (h *head) follow(line int) {
	if line < h.top {
		h.top = line
	} else if h.room > 0 && line >= h.top+h.room {
		h.top = line - h.room + 1
	}
}

// fit puts lines above tail within height, from the scrolled position, and
// reports whether lines were cut.
func (h *head) fit(lines, tail []string, height int) (body []string, cut bool) {
	h.room = max(height-len(tail)-1, 1)
	gap := ""
	if len(lines) > h.room {
		h.top = min(h.top, len(lines)-h.room)
		gap = theme.SubtleText.Render(fmt.Sprintf("lines %d–%d of %d", h.top+1, h.top+h.room, len(lines)))
		lines, cut = lines[h.top:h.top+h.room], true
	}
	body = append(slices.Clone(lines), gap)
	return append(body, tail...), cut
}

// frame cuts the body to fit height.
func frame(title string, body []string, hint string, width, height int) string {
	head := theme.FaintText.Render("── ") + theme.Selected.Render(title) + " "
	head += theme.FaintText.Render(strings.Repeat("─", max(width-ansi.StringWidth(head), 0)))
	lines := []string{ansi.Truncate(head, width, "")}
	room := height - 1
	if hint != "" {
		room--
	}
	if len(body) > room {
		body = body[:max(room, 0)]
	}
	for _, l := range body {
		lines = append(lines, ansi.Truncate(" "+l, width, "…"))
	}
	if hint != "" {
		lines = append(lines, ansi.Truncate(" "+hint, width, "…"))
	}
	return strings.Join(lines, "\n")
}

func row(text string, selected bool) string {
	if selected {
		return theme.Selected.Render("❯ ") + inline(text, theme.Selected)
	}
	return "  " + inline(text, theme.Text)
}

// inline renders `code` spans without backticks, in the code color.
func inline(text string, st lipgloss.Style) string {
	var b strings.Builder
	for i, part := range strings.Split(text, "`") {
		if i%2 == 1 {
			b.WriteString(st.Foreground(theme.Info).Render(part))
		} else {
			b.WriteString(st.Render(part))
		}
	}
	return b.String()
}

func window(n, cursor, top, room int) (from, to int) {
	if room <= 0 || n <= room {
		return 0, n
	}
	// Stay near top while keeping the cursor and the window in range.
	top = min(max(top, cursor-room+1, 0), cursor, n-room)
	return top, top + room
}

func key(msg tea.Msg) (string, bool) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return "", false
	}
	return k.String(), true
}
