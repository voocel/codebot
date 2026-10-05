// Package panel holds what takes the bottom of the screen in place of the
// editor until the user is done with it: a permission request, questions,
// a list to pick from, a page of text.
package panel

import (
	"strings"

	tea "charm.land/bubbletea/v2"
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

// Keyed is a Panel that answers a request the asker can withdraw; Key
// identifies the request.
type Keyed interface {
	Key() any
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
		return theme.Selected.Render("❯ ") + theme.Selected.Render(text)
	}
	return "  " + theme.Text.Render(text)
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
