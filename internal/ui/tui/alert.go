package tui

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The user may leave codebot working. The terminal's title says what it
// is up to, and once the terminal reports the user has gone to another
// window, codebot alerts them when it needs them or is done. A terminal
// that does not report focus gets no alerts.

// title names the terminal's window or tab, marked while codebot works and
// while it waits for the user.
func (m *Model) title() string {
	t := "codebot · " + filepath.Base(m.status.Cwd)
	switch {
	case isRequest(m.top()):
		return "✋ " + t
	case m.run.active:
		return "● " + t
	}
	return t
}

// alert raises text where the user away will see it.
func (m *Model) alert(text string) tea.Cmd {
	if !m.away {
		return nil
	}
	return tea.Raw(notification(text, os.Getenv))
}

// notification is what raises text in the terminal getenv describes: the
// bell, which terminals flag the window with, after a desktop notification
// in those known to show one. That one needs the system's permission, which
// the terminal may not have, so the bell rings with it.
func notification(text string, getenv func(string) string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	return desktop(text, getenv) + "\a"
}

// desktop is what shows text as a desktop notification in the terminal
// getenv describes, "" where none is known to show.
func desktop(text string, getenv func(string) string) string {
	switch {
	case getenv("TMUX") != "":
		return "" // tmux keeps it to itself
	case getenv("KITTY_WINDOW_ID") != "":
		return ansi.DesktopNotification("codebot: " + text)
	}
	switch getenv("TERM_PROGRAM") {
	case "iTerm.app":
		return ansi.Notify("codebot: " + text)
	case "ghostty", "WezTerm":
		return ansi.URxvtExt("notify", "codebot", text)
	}
	return ""
}
