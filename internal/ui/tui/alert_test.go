package tui

import (
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/interact"
)

// The title says what codebot is up to; the user is alerted to what needs
// them only once away from the terminal.
func TestAlertsTheUserAway(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("TMUX", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	h := boot(t)
	title := "codebot · " + filepath.Base(h.app.Cwd())
	if got := h.m.View().WindowTitle; got != title {
		t.Errorf("the title is %q", got)
	}

	ask := func() string {
		return raw(h.m.update(approveMsg{interact.Approval{Tool: "bash"}, make(chan interact.Verdict, 1)}))
	}
	if got := ask(); got != "" {
		t.Errorf("alerted the user at the terminal: %q", got)
	}
	if got := h.m.View().WindowTitle; got != "✋ "+title {
		t.Errorf("waiting, the title is %q", got)
	}
	h.feed(tea.BlurMsg{})
	if got := ask(); got != ansi.Notify("codebot: Allow Bash?")+"\a" {
		t.Errorf("alerted the user away with %q", got)
	}
	h.feed(tea.FocusMsg{})
	if got := ask(); got != "" {
		t.Errorf("alerted the user back with %q", got)
	}
}

// raw returns what cmd writes to the terminal as it is.
func raw(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	switch msg := cmd().(type) {
	case tea.RawMsg:
		return msg.Msg.(string)
	case tea.BatchMsg:
		var out string
		for _, c := range msg {
			out += raw(c)
		}
		return out
	}
	return ""
}

func TestNotification(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, "\x1b]9;codebot: Done\x07\a"},
		{map[string]string{"TERM_PROGRAM": "ghostty"}, "\x1b]777;notify;codebot;Done\x07\a"},
		{map[string]string{"KITTY_WINDOW_ID": "1"}, "\x1b]99;;codebot: Done\x07\a"},
		{map[string]string{"TERM_PROGRAM": "iTerm.app", "TMUX": "/tmp/tmux"}, "\a"},
		{map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, "\a"},
	} {
		if got := notification("Do\x1b\ane", func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%v: %q, want %q", c.env, got, c.want)
		}
	}
}
