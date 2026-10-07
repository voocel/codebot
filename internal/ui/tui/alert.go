package tui

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Alerts fire only after the terminal reports losing focus, so terminals
// that don't report focus get none.

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

func (m *Model) alert(text string) tea.Cmd {
	if !m.away {
		return nil
	}
	return tea.Raw(notification(text, os.Getenv))
}

// notification always rings the bell, which terminals flag the window with,
// because a desktop notification needs OS permission the terminal may lack.
func notification(text string, getenv func(string) string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	return desktop(text, getenv) + "\a"
}

// desktop returns "" for terminals not known to show desktop notifications.
func desktop(text string, getenv func(string) string) string {
	switch {
	case getenv("TMUX") != "":
		return "" // tmux swallows it
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
