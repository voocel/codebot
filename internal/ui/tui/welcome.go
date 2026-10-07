package tui

import (
	"cmp"
	"image/color"
	"math/rand/v2"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

const tagline = "A long-running coding agent that lives in your terminal."

// Full blocks are painted as background color, since terminals leave no
// gaps between rows in a background.
var bot = [4]string{
	"  ▀▄    ▄▀  ",
	" ▄████████▄ ",
	" ██  ██  ██ ",
	" ▀████████▀ ",
}

var tips = []string{
	"Drag across text to copy it",
	"Click a tool call to open it up · ctrl+o opens them all",
	"ctrl+t opens the whole conversation in your pager",
	"Start a line with ! to run a shell command",
	"shift+tab switches how much codebot asks before acting",
	"/btw asks a side question that stays out of the conversation",
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
	cardWidth = 72
	cardPad   = 2  // columns between the border and the content
	botGap    = 4  // columns between the bot and the text beside it
	minBeside = 20 // below this many columns for the text, the bot is hidden
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
	w := min(width, cardWidth)
	inner := w - 2 - 2*cardPad
	if inner < 1 {
		return nil
	}
	head := m.head(inner)
	var motto []string
	for _, l := range strings.Split(ansi.Wordwrap(tagline, inner, ""), "\n") {
		motto = append(motto, centre([]string{theme.MutedText.Render(l)}, inner)...)
	}
	recent, ids := m.recentRows(inner)
	tip := m.tipLines(inner)
	ink := lipgloss.Blend1D(w, sink(theme.Accent), sink(theme.Info), sink(theme.Agent))

	// From the fullest layout to the most compact.
	for _, c := range []struct{ recent, tip, roomy bool }{
		{true, true, true},
		{true, false, true},
		{false, false, true},
		{false, false, false},
	} {
		k := newCard(ink)
		if c.roomy {
			k.add("")
		}
		k.add(head...)
		if c.roomy {
			k.add("")
		}
		k.add(motto...)
		if c.roomy {
			k.add("")
		}
		at := map[int]string{}
		if c.recent && len(recent) > 0 {
			k.section("Recent", "click to resume")
			for i, row := range recent {
				at[len(k.lines)] = ids[i]
				k.add(row)
			}
		}
		lines := k.close()
		if c.tip {
			// Align with the card's content.
			margin := strings.Repeat(" ", 1+cardPad)
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
		return append(make([]string, top), centre(lines, width)...)
	}
	return nil
}

func (m *Model) head(width int) []string {
	beside := width - ansi.StringWidth(bot[0]) - botGap
	if beside < minBeside {
		beside = width
	}
	st := m.status
	model := theme.Text.Render(st.Model)
	// Drop the effort when it doesn't fit beside the model.
	if effort := theme.FaintText.Render("  ·  ") + theme.SubtleText.Render("effort "+cmp.Or(st.Effort, "auto")); st.Reasoning && ansi.StringWidth(model+effort) <= beside {
		model += effort
	}
	lines := []string{
		theme.Bold.Foreground(theme.Strong).Render("codebot") + theme.SubtleText.Render("  "+m.versionLabel()),
		model,
		m.place(beside),
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, beside, "…")
	}
	if beside < width {
		face := drawBot()
		gap := strings.Repeat(" ", botGap)
		// The text sits beside the head, below the antennae.
		lines = append([]string{face[0]}, lines...)
		for i := 1; i < len(face); i++ {
			lines[i] = face[i] + gap + lines[i]
		}
	}
	return centre(lines, width)
}

func drawBot() []string {
	w := ansi.StringWidth(bot[0])
	ramp := lipgloss.Blend1D(w+2*len(bot), theme.Accent, theme.Info)
	out := make([]string, len(bot))
	for y, row := range bot {
		var b strings.Builder
		for x, r := range []rune(row) {
			c := ramp[x+2*y] // a row is about two columns tall
			switch r {
			case ' ':
				b.WriteByte(' ')
			case '█':
				b.WriteString(lipgloss.NewStyle().Background(c).Render(" "))
			default:
				b.WriteString(lipgloss.NewStyle().Foreground(c).Render(string(r)))
			}
		}
		out[y] = b.String()
	}
	return out
}

func centre(lines []string, width int) []string {
	w := 0
	for _, l := range lines {
		w = max(w, ansi.StringWidth(l))
	}
	pad := strings.Repeat(" ", max(width-w, 0)/2)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Truncate(pad+l, width, "…")
	}
	return out
}

// card is a rounded box whose border takes one color of ink per column.
type card struct {
	ink   []color.Color
	lines []string
}

func newCard(ink []color.Color) *card {
	k := &card{ink: ink}
	k.lines = []string{k.edge("╭", "╮", "", "")}
	return k
}

func (k *card) add(lines ...string) {
	w := len(k.ink)
	pad := strings.Repeat(" ", cardPad)
	for _, l := range lines {
		fill := strings.Repeat(" ", max(w-2-2*cardPad-ansi.StringWidth(l), 0))
		k.lines = append(k.lines, k.stroke("│", 0)+pad+l+fill+pad+k.stroke("│", w-1))
	}
}

func (k *card) section(title, note string) {
	k.lines = append(k.lines, k.edge("├", "┤", title, note))
}

func (k *card) close() []string {
	return append(k.lines, k.edge("╰", "╯", "", ""))
}

func (k *card) edge(l, r, title, note string) string {
	w := len(k.ink)
	if title == "" || w-2 < ansi.StringWidth(title)+4 {
		return k.stroke(l+strings.Repeat("─", w-2)+r, 0)
	}
	head := k.stroke(l+"─ ", 0) + theme.MutedText.Bold(true).Render(title)
	x := 3 + ansi.StringWidth(title)
	if fill := w - x - ansi.StringWidth(note) - 5; note != "" && fill >= 2 {
		return head + k.stroke(" "+strings.Repeat("─", fill)+" ", x) +
			theme.SubtleText.Render(note) + k.stroke(" ─"+r, w-3)
	}
	return head + k.stroke(" "+strings.Repeat("─", w-x-2)+r, x)
}

func (k *card) stroke(s string, x int) string {
	var b strings.Builder
	for i, r := range []rune(s) {
		b.WriteString(lipgloss.NewStyle().Foreground(k.ink[x+i]).Render(string(r)))
	}
	return b.String()
}

func sink(c color.Color) color.Color {
	return lipgloss.Blend1D(3, c, theme.Faint)[1]
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
