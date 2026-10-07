package panel

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/ui/tui/theme"
)

type Text struct {
	Title string
	Tabs  []Tab
	// Load, if set, runs off the TUI goroutine; the panel shows a loading
	// state until it returns.
	Load func() []Tab

	active  int
	offset  int
	loading bool
	height  int // last view's height, for paging
}

type Tab struct {
	Name string
	Body func(width int) []string
}

func Lines(lines ...string) func(int) []string {
	return func(int) []string { return lines }
}

type loadedMsg struct {
	t    *Text
	tabs []Tab
}

func (t *Text) Init() tea.Cmd {
	if t.Load == nil {
		return nil
	}
	t.loading = true
	load := t.Load
	return func() tea.Msg { return loadedMsg{t, load()} }
}

func (t *Text) Update(msg tea.Msg) (tea.Cmd, bool) {
	if m, ok := msg.(loadedMsg); ok {
		if m.t == t {
			t.Tabs, t.loading = m.tabs, false
		}
		return nil, false
	}
	k, ok := key(msg)
	if !ok {
		return nil, false
	}
	page := max(t.height-3, 1)
	switch k {
	case "esc", "q", "enter", "ctrl+c":
		return nil, true
	case "tab", "right", "l":
		if len(t.Tabs) > 1 {
			t.active, t.offset = (t.active+1)%len(t.Tabs), 0
		}
	case "shift+tab", "left", "h":
		if len(t.Tabs) > 1 {
			t.active, t.offset = (t.active+len(t.Tabs)-1)%len(t.Tabs), 0
		}
	case "up", "k":
		t.offset--
	case "down", "j":
		t.offset++
	case "pgup":
		t.offset -= page
	case "pgdown", "space":
		t.offset += page
	case "home", "g":
		t.offset = 0
	}
	t.offset = max(t.offset, 0)
	return nil, false
}

func (t *Text) View(width, height int) string {
	t.height = height
	var body []string
	if len(t.Tabs) > 1 {
		var tabs []string
		for i, tab := range t.Tabs {
			if i == t.active {
				tabs = append(tabs, theme.Selected.Underline(true).Render(tab.Name))
			} else {
				tabs = append(tabs, theme.MutedText.Render(tab.Name))
			}
		}
		body = append(body, strings.Join(tabs, theme.FaintText.Render("  ·  ")), "")
	}

	var lines []string
	switch {
	case t.loading:
		lines = []string{theme.SubtleText.Render("Loading…")}
	case len(t.Tabs) > 0:
		lines = t.Tabs[t.active].Body(width - 2)
	}
	room := max(height-2-len(body), 1)
	t.offset = min(t.offset, max(len(lines)-room, 0))
	end := min(t.offset+room, len(lines))
	body = append(body, lines[t.offset:end]...)

	pairs := []string{"esc", "close"}
	if len(lines) > room {
		pairs = append([]string{"↑↓", "scroll"}, pairs...)
	}
	if len(t.Tabs) > 1 {
		pairs = append([]string{"tab", "switch"}, pairs...)
	}
	return frame(t.Title, body, theme.Hint(pairs...), width, height)
}
