package tui

import (
	"cmp"
	"math/rand/v2"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/brand"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

const tagline = "A long-running coding agent that lives in your terminal."

var tips = []string{
	"Drag across text to copy it",
	"Click a tool call to open it up · ctrl+o opens them all",
	"ctrl+t opens the whole conversation in your pager",
	"Start a line with ! to run a shell command",
	"shift+tab switches how much codebot asks before acting",
	"/btw asks a side question that stays out of the conversation",
	"/rewind goes back to before a request, its file changes too",
	"/init writes AGENTS.md, what every request here starts from",
	"ctrl+v pastes an image from the clipboard",
	"@ mentions a file · tab completes its path",
	"ctrl+r searches what you sent before",
	"ctrl+g opens the input in your editor",
	"Type while codebot works: your message joins at its next step",
	"esc stops the run · what you queued comes back to the editor",
}

const (
	maxRecent = 3
	maxPath   = 40 // columns
)

type recentMsg struct{ sessions []app.SessionInfo }

// loadRecent reads session files, so it runs off the TUI goroutine.
func (m *Model) loadRecent() tea.Cmd {
	a := m.app
	return func() tea.Msg {
		sessions, err := a.Sessions()
		if err != nil {
			return nil
		}
		return recentMsg{sessions}
	}
}

// setRecent skips the open session and empty ones.
func (m *Model) setRecent(sessions []app.SessionInfo) {
	m.recent = m.recent[:0]
	for _, s := range sessions {
		if s.ID != m.conv.ID() && s.MessageCount > 0 && len(m.recent) < maxRecent {
			m.recent = append(m.recent, s)
		}
	}
}

// welcome drops the tip first, then the recent sessions, when space runs
// out.
func (m *Model) welcome(width, height int) []string {
	m.recentAt = nil
	w := min(width, brand.MaxWidth)
	inner := brand.Inner(w)
	if inner < 1 {
		return nil
	}
	head := m.head(inner)
	var motto []string
	for _, l := range strings.Split(ansi.Wordwrap(tagline, inner, ""), "\n") {
		motto = append(motto, brand.Center([]string{theme.MutedText.Render(l)}, inner)...)
	}
	recent, ids := m.recentRows(inner)
	tip := m.tipLines(inner)

	// From the fullest layout to the most compact.
	for _, c := range []struct{ recent, tip, roomy bool }{
		{true, true, true},
		{true, false, true},
		{false, false, true},
		{false, false, false},
	} {
		k := brand.NewCard(w)
		if c.roomy {
			k.Add("")
		}
		k.Add(head...)
		if c.roomy {
			k.Add("")
		}
		k.Add(motto...)
		if c.roomy {
			k.Add("")
		}
		at := map[int]string{}
		if c.recent && len(recent) > 0 {
			k.Section(theme.MutedText.Bold(true).Render("Recent"), "click to resume")
			for i, row := range recent {
				at[k.Lines()] = ids[i]
				k.Add(row)
			}
		}
		lines := k.Close()
		if c.tip {
			// Align with the card's content.
			margin := strings.Repeat(" ", 1+brand.Pad)
			lines = append(lines, "")
			for _, l := range tip {
				lines = append(lines, margin+l)
			}
		}
		if len(lines) > height {
			continue
		}
		top := (height - len(lines)) * 2 / 5
		m.recentAt = map[int]string{}
		for row, id := range at {
			m.recentAt[top+row] = id
		}
		return append(make([]string, top), brand.Center(lines, width)...)
	}
	return nil
}

func (m *Model) head(width int) []string {
	beside := brand.Room(width)
	st := m.status
	model := theme.Text.Render(st.Model)
	// Drop the effort when it doesn't fit beside the model.
	if effort := theme.FaintText.Render("  ·  ") + theme.SubtleText.Render("effort "+cmp.Or(st.Effort, "auto")); st.Reasoning && ansi.StringWidth(model+effort) <= beside {
		model += effort
	}
	return brand.Head([]string{
		theme.Bold.Foreground(theme.Strong).Render("codebot") + theme.SubtleText.Render("  "+m.versionLabel()),
		model,
		m.place(beside),
	}, width)
}

func (m *Model) versionLabel() string {
	if m.version != "" && m.version[0] >= '0' && m.version[0] <= '9' {
		return "v" + m.version
	}
	return m.version
}

func (m *Model) place(width int) string {
	path := transcript.HomePath(m.status.Cwd)
	var branch string
	if m.branch != "" {
		branch = theme.FaintText.Render("  ·  ") + theme.MutedText.Render("⎇ "+m.branch)
	}
	// Keep the end of a long path. When room is short, drop the branch,
	// which the footer shows too.
	room := width - ansi.StringWidth(branch)
	if room < min(ansi.StringWidth(path), maxPath/2) {
		branch, room = "", width
	}
	room = min(room, maxPath)
	if w := ansi.StringWidth(path); w > room {
		path = ansi.TruncateLeft(path, w-room+1, "…")
	}
	return theme.PathText.Render(path) + branch
}

func (m *Model) tipLines(width int) []string {
	const lead = "Tip  "
	lines := strings.Split(ansi.Wordwrap(m.tip, width-len(lead), ""), "\n")
	for i, l := range lines {
		indent := strings.Repeat(" ", len(lead))
		if i == 0 {
			indent = theme.AccentText.Render("Tip") + "  "
		}
		lines[i] = indent + theme.SubtleText.Render(l)
	}
	return lines
}

func (m *Model) recentRows(width int) (rows, ids []string) {
	now := time.Now()
	for _, s := range m.recent {
		when := transcript.Ago(now.Sub(s.Updated))
		title := strings.Join(strings.Fields(s.FirstMessage), " ")
		title = ansi.Truncate(title, max(width-ansi.StringWidth(when)-2, 1), "…")
		gap := max(width-ansi.StringWidth(title)-ansi.StringWidth(when), 1)
		rows = append(rows, theme.Text.Render(title)+strings.Repeat(" ", gap)+theme.SubtleText.Render(when))
		ids = append(ids, s.ID)
	}
	return rows, ids
}

func randomTip() string { return tips[rand.N(len(tips))] }
