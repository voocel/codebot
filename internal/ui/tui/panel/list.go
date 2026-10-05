package panel

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// Item is a row of a List.
type Item struct {
	Title  string
	Detail string // shown muted after the title
	// Group heads the items that share it; a new group starts a section.
	Group string
	// Current marks the item in use, such as the model selected.
	Current bool
	Value   any
}

// List lets the user pick an item.
type List struct {
	Title string
	Items []Item
	// Hint replaces the default hint.
	Hint string
	// Filter narrows the items to those matching what the user types.
	Filter bool
	// Select runs when the user picks an item with enter; the list closes.
	Select func(Item) tea.Cmd
	// Keys handles keys the list does not: handled stops the list from
	// handling it, done closes the list. item is the selected item, nil
	// when none is.
	Keys func(k string, item *Item) (cmd tea.Cmd, handled, done bool)
	// Reload, when set, reads the items again every second while shown.
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

// visible returns the items that match the query.
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

	var rows []string
	selectedRow := 0
	group := ""
	for i, it := range items {
		if it.Group != "" && it.Group != group {
			group = it.Group
			if len(rows) > 0 {
				rows = append(rows, "")
			}
			rows = append(rows, theme.MutedText.Bold(true).Render(group))
		}
		if i == l.cursor {
			selectedRow = len(rows)
		}
		rows = append(rows, l.row(it, i == l.cursor, width-1))
	}
	if len(items) == 0 {
		rows = []string{theme.SubtleText.Render("Nothing matches")}
	}

	var body []string
	if l.Filter {
		q := theme.SubtleText.Render("Type to filter")
		if l.query != "" {
			q = theme.AccentText.Render("/ ") + theme.Text.Render(l.query)
		}
		body = append(body, q)
	}
	room := max(height-2-len(body), 1)
	from, to := window(len(rows), selectedRow, l.top, room)
	l.top = from
	body = append(body, rows[from:to]...)

	hint := l.Hint
	if hint == "" {
		hint = theme.Hint("↑↓", "select", "enter", "choose", "esc", "close")
	}
	return frame(l.Title, body, hint, width, height)
}

func (l *List) row(it *Item, selected bool, width int) string {
	title := it.Title
	if it.Current {
		title += " ✓"
	}
	line := row(title, selected)
	if it.Detail != "" {
		line += "  " + theme.SubtleText.Render(it.Detail)
	}
	return ansi.Truncate(line, width, "…")
}
