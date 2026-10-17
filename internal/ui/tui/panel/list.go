package panel

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/ui/tui/theme"
)

type Item struct {
	Title  string
	Detail string // shown muted after the title
	// Cells follow the title in columns, under the list's Header.
	Cells []string
	// A change of Group starts a new section.
	Group string
	// Current marks the item in use, such as the selected model.
	Current bool
	Value   any
}

type List struct {
	Title string
	Items []Item
	// Header names the columns of the items' Cells.
	Header []string
	// Notes describe groups beside their names.
	Notes map[string]string
	// Footer tells about the selected item, below the items.
	Footer func(*Item) string
	Hint   string // replaces the default hint
	Filter bool
	// Select runs on enter, and the list closes.
	Select func(Item) tea.Cmd
	// Keys sees keys first. handled stops the list from processing the key;
	// done closes the list. item is nil when nothing is selected.
	Keys func(k string, item *Item) (cmd tea.Cmd, handled, done bool)
	// Reload, if set, refreshes the items every second while shown.
	Reload func() []Item

	cursor int
	top    int
	query  string
}

type reloadMsg struct{ l *List }

func (l *List) Init() tea.Cmd {
	l.cursor = max(0, slices.IndexFunc(l.Items, func(it Item) bool { return it.Current }))
	return l.tick()
}

func (l *List) tick() tea.Cmd {
	if l.Reload == nil {
		return nil
	}
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return reloadMsg{l} })
}

func (l *List) visible() []*Item {
	q := strings.ToLower(l.query)
	var out []*Item
	for i := range l.Items {
		it := &l.Items[i]
		if q == "" || strings.Contains(strings.ToLower(it.Title+" "+it.Detail+" "+it.Group), q) {
			out = append(out, it)
		}
	}
	return out
}

func (l *List) selected() *Item {
	items := l.visible()
	if len(items) == 0 {
		return nil
	}
	return items[min(l.cursor, len(items)-1)]
}

func (l *List) Update(msg tea.Msg) (tea.Cmd, bool) {
	if m, ok := msg.(reloadMsg); ok {
		if m.l != l {
			return nil, false
		}
		l.Items = l.Reload()
		return l.tick(), false
	}
	k, ok := key(msg)
	if !ok {
		return nil, false
	}
	if l.Keys != nil {
		if cmd, handled, done := l.Keys(k, l.selected()); handled {
			return cmd, done
		}
	}
	n := len(l.visible())
	switch k {
	case "up", "ctrl+p":
		l.cursor = max(l.cursor-1, 0)
	case "down", "ctrl+n":
		l.cursor = min(l.cursor+1, max(n-1, 0))
	case "pgup":
		l.cursor = max(l.cursor-10, 0)
	case "pgdown":
		l.cursor = min(l.cursor+10, max(n-1, 0))
	case "enter":
		it := l.selected()
		if it == nil || l.Select == nil {
			return nil, it == nil
		}
		return l.Select(*it), true
	case "esc", "ctrl+c":
		if l.query != "" {
			l.query, l.cursor = "", 0
			return nil, false
		}
		return nil, true
	case "backspace":
		if l.Filter && l.query != "" {
			r := []rune(l.query)
			l.query, l.cursor = string(r[:len(r)-1]), 0
		}
	default:
		if kp := msg.(tea.KeyPressMsg); l.Filter && kp.Text != "" {
			l.query += kp.Text
			l.cursor = 0
		} else if !l.Filter && (k == "k" || k == "j") {
			if k == "k" {
				l.cursor = max(l.cursor-1, 0)
			} else {
				l.cursor = min(l.cursor+1, max(n-1, 0))
			}
		}
	}
	return nil, false
}

func (l *List) View(width, height int) string {
	items := l.visible()
	l.cursor = min(l.cursor, max(len(items)-1, 0))
	cols := l.columns(width - 1)

	var rows []string
	selectedRow := 0
	group := ""
	for i, it := range items {
		if it.Group != "" && it.Group != group {
			group = it.Group
			if len(rows) > 0 {
				rows = append(rows, "")
			}
			head := theme.MutedText.Bold(true).Render(group)
			if note := l.Notes[group]; note != "" {
				head += "  " + theme.SubtleText.Render(note)
			}
			rows = append(rows, ansi.Truncate(head, width-1, "…"))
		}
		if i == l.cursor {
			selectedRow = len(rows)
		}
		rows = append(rows, l.row(it, i == l.cursor, cols, width-1))
	}
	if len(items) == 0 {
		rows = []string{theme.SubtleText.Render("Nothing matches")}
	}

	var head, tail []string
	if l.Filter || cols != nil {
		head = []string{l.heading(cols, width-1)}
		// A group's name right under the heading would read as part of it.
		if len(items) > 0 && items[0].Group != "" {
			head = append(head, "")
		}
	}
	if it := l.selected(); it != nil && l.Footer != nil {
		tail = []string{"", l.Footer(it)}
	}
	room := max(height-chrome-len(head)-len(tail), 1)
	from, to := window(len(rows), selectedRow, l.top, room)
	l.top = from
	body := append(append(head, rows[from:to]...), tail...)

	hint := l.Hint
	if hint == "" {
		hint = theme.Hint("↑↓", "select", "enter", "choose", "esc", "close")
	}
	return frame(l.Title, body, hint, width, height)
}

// gap sets columns apart.
const gap = 3

// heading is the filter, then the names of the columns.
func (l *List) heading(cols []int, width int) string {
	line := ""
	if l.Filter {
		line = theme.SubtleText.Render("Type to filter")
		if l.query != "" {
			line = theme.AccentText.Render("/ ") + theme.Text.Render(l.query)
		}
	}
	if cols == nil {
		return line
	}
	line = ansi.Truncate(line, cols[0]-1, "…")
	return line + strings.Repeat(" ", cols[0]-ansi.StringWidth(line)) + cells(l.Header, cols[1:], theme.SubtleText)
}

// columns returns the width of the titles, with the cursor before them, then
// of each column of cells; nil when the items have no cells, or the cells do
// not fit.
func (l *List) columns(width int) []int {
	cols := []int{0}
	fit := func(i int, s string) {
		for len(cols) <= i {
			cols = append(cols, 0)
		}
		cols[i] = max(cols[i], ansi.StringWidth(s))
	}
	for i, h := range l.Header {
		fit(i+1, h)
	}
	for _, it := range l.Items {
		fit(0, title(&it, false))
		for i, c := range it.Cells {
			fit(i+1, c)
		}
	}
	total := cols[0]
	for _, w := range cols[1:] {
		total += gap + w
	}
	if len(cols) == 1 || total > width {
		return nil
	}
	return cols
}

func (l *List) row(it *Item, selected bool, cols []int, width int) string {
	line := title(it, selected)
	if cols != nil {
		line += strings.Repeat(" ", cols[0]-ansi.StringWidth(line)) + cells(it.Cells, cols[1:], theme.MutedText)
	}
	if it.Detail != "" {
		line += "  " + theme.SubtleText.Render(it.Detail)
	}
	return ansi.Truncate(line, width, "…")
}

func title(it *Item, selected bool) string {
	t := row(it.Title, selected)
	if it.Current {
		t += theme.OKText.Render(" ✓")
	}
	return t
}

// cells right-aligns each cell in its column.
func cells(cs []string, widths []int, st lipgloss.Style) string {
	var b strings.Builder
	for i, w := range widths {
		c := ""
		if i < len(cs) {
			c = cs[i]
		}
		b.WriteString(strings.Repeat(" ", gap+w-ansi.StringWidth(c)))
		b.WriteString(st.Render(c))
	}
	return b.String()
}
