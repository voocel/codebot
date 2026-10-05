// Package panel holds what takes the bottom of the screen in place of the
// editor until the user is done with it: a permission request, questions,
// a list to pick from, a page of text.
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

// A Panel takes the keys and the bottom of the screen while it is shown.
type Panel interface {
	// Update takes a message: the keys, and any message the TUI does not
	// handle itself. done reports that the panel is finished with.
	Update(msg tea.Msg) (cmd tea.Cmd, done bool)
	// View renders the panel at most height lines tall.
	View(width, height int) string
}

// Initer is a Panel with work to start once shown.
type Initer interface {
	Init() tea.Cmd
}

// A Request is a Panel that answers what the agent asked, which the asker
// may withdraw: Key identifies it. Requests wait their turn; Queue tells
// the one shown how many wait behind it.
type Request interface {
	Panel
	Key() any
	Queue(behind int)
}

// queue is what a request knows of those behind it, for its title.
type queue struct{ behind int }

func (q *queue) Queue(behind int) { q.behind = behind }

func (q queue) title(t string) string {
	if q.behind > 0 {
		return t + " · " + strconv.Itoa(q.behind) + " more"
	}
	return t
}

// menu is a column of options under a cursor: ↑↓ move it, and enter or an
// option's number picks one.
type menu struct{ n, cursor int }

// key moves the cursor or picks an option: it returns the option picked,
// or -1.
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

// numbered renders option i as "1. text", marked when under the cursor.
func (m menu) numbered(i int, text string) string {
	return row(strconv.Itoa(i+1)+". "+text, i == m.cursor)
}

// field is a line the user types an answer of their own in, in place of
// an option.
type field struct {
	input textinput.Model
	on    bool
}

func newField(placeholder string) field {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = placeholder
	return field{input: in}
}

// open shows the field, holding value.
func (f *field) open(value string) tea.Cmd {
	f.on = true
	f.input.SetValue(value)
	f.input.CursorEnd()
	return f.input.Focus()
}

// update takes msg while the field is open. Enter closes it and reports
// what was typed; esc only closes it.
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

// head is what a request says above its options. What does not fit is
// cut, and pgup and pgdown scroll it.
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

// fit lays lines out above tail in height lines, a line between them, from
// the line scrolled to. It reports whether lines were cut.
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

// frame lays a panel out: a rule with the title, the body, then the hint,
// within height lines; the body is cut to fit.
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

// row renders a selectable line, marked when selected.
func row(text string, selected bool) string {
	if selected {
		return theme.Selected.Render("❯ ") + inline(text, theme.Selected)
	}
	return "  " + inline(text, theme.Text)
}

// inline renders text in st, its `code` spans without the backticks and in
// the colour of code.
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

// window returns the range of n rows to show in room so that cursor shows.
func window(n, cursor, top, room int) (from, to int) {
	if room <= 0 || n <= room {
		return 0, n
	}
	// As close to top as keeps the cursor in view, and the window in n.
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
