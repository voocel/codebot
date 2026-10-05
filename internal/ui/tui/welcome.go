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

// The welcome is what an empty conversation shows, a little above the
// middle: a card holding the bot beside where codebot works, the tagline
// under them, and the recent conversations, a click away, then a tip. What does not fit
// goes: the tip first, then the recent conversations.

const tagline = "A long-running coding agent that lives in your terminal."

// bot is codebot's face: antennae, a round head and two eyes. Its full
// cells are painted as background, which no terminal leaves gaps in between
// rows, and its edges are half blocks.
var bot = [4]string{
	"  ▀▄    ▄▀  ",
	" ▄████████▄ ",
	" ██  ██  ██ ",
	" ▀████████▀ ",
}

// tips teach what is not in sight.
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
	maxRecent = 3  // recent conversations listed
	maxPath   = 40 // columns of the directory shown
	cardWidth = 72 // the widest the card gets
	cardPad   = 2  // columns between the card's border and what it holds
	botGap    = 4  // columns between the bot and the lines beside it
	minBeside = 20 // the fewest columns the bot leaves the lines beside it
)

type recentMsg struct{ sessions []app.SessionInfo }

// loadRecent reads the conversations of the workspace, which means reading
// their files, so off the TUI's goroutine.
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

// setRecent keeps the conversations worth going back to: the latest that
// say something, but the one open.
func (m *Model) setRecent(sessions []app.SessionInfo) {
	m.recent = m.recent[:0]
	for _, s := range sessions {
		if s.ID != m.conv.ID() && s.MessageCount > 0 && len(m.recent) < maxRecent {
			m.recent = append(m.recent, s)
		}
	}
}

// welcome lays the welcome out in width by height, and notes the line of
// each recent conversation for the mouse.
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

	// From the most to the least that may show.
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
			// In line with what the card holds.
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

// head is the top of the card, centred in width: the bot, and beside its
// head the name, the model and the place; those alone when the bot leaves
// them too little room.
func (m *Model) head(width int) []string {
	beside := width - ansi.StringWidth(bot[0]) - botGap
	if beside < minBeside {
		beside = width
	}
	st := m.status
	model := theme.Text.Render(st.Model)
	// The effort, short of room, makes way for the model.
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
		// The lines sit beside the head, under the antennae.
		lines = append([]string{face[0]}, lines...)
		for i := 1; i < len(face); i++ {
			lines[i] = face[i] + gap + lines[i]
		}
	}
	return centre(lines, width)
}

// drawBot paints the bot in a light running from the accent at its top
// left to blue at its bottom right.
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

// centre indents lines as one block to the middle of width.
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

// card is a box with rounded corners, its border running through the
// colours of ink, one per column.
type card struct {
	ink   []color.Color
	lines []string
}

func newCard(ink []color.Color) *card {
	k := &card{ink: ink}
	k.lines = []string{k.edge("╭", "╮", "", "")}
	return k
}

// add puts lines in the card, each at most its inner width wide.
func (k *card) add(lines ...string) {
	w := len(k.ink)
	pad := strings.Repeat(" ", cardPad)
	for _, l := range lines {
		fill := strings.Repeat(" ", max(w-2-2*cardPad-ansi.StringWidth(l), 0))
		k.lines = append(k.lines, k.stroke("│", 0)+pad+l+fill+pad+k.stroke("│", w-1))
	}
}

// section opens a part of the card under a rule bearing title and note.
func (k *card) section(title, note string) {
	k.lines = append(k.lines, k.edge("├", "┤", title, note))
}

// close draws the card's bottom and returns its lines.
func (k *card) close() []string {
	return append(k.lines, k.edge("╰", "╯", "", ""))
}

// edge draws a border line between the corners l and r, with title and
// note set into it where they fit.
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

// stroke draws the border runes of s from column x on, each in its
// column's colour.
func (k *card) stroke(s string, x int) string {
	var b strings.Builder
	for i, r := range []rune(s) {
		b.WriteString(lipgloss.NewStyle().Foreground(k.ink[x+i]).Render(string(r)))
	}
	return b.String()
}

// sink takes c halfway to the colour of rules, for the card's border.
func sink(c color.Color) color.Color {
	return lipgloss.Blend1D(3, c, theme.Faint)[1]
}

func (m *Model) versionLabel() string {
	if m.version != "" && m.version[0] >= '0' && m.version[0] <= '9' {
		return "v" + m.version
	}
	return m.version
}

// place says where the conversation works, in width: the directory and the
// branch.
func (m *Model) place(width int) string {
	path := transcript.HomePath(m.status.Cwd)
	var branch string
	if m.branch != "" {
		branch = theme.FaintText.Render("  ·  ") + theme.MutedText.Render("⎇ "+m.branch)
	}
	// The end of a long path says the most. Short of room, the branch,
	// which the footer shows too, makes way for it.
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

// tipLines wraps the tip to width.
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

// recentRows lists the recent conversations width wide, and the id of each.
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
