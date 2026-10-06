package tui

import (
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

func (m *Model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	v.WindowTitle = m.title()
	if m.width == 0 || m.height == 0 {
		return v
	}
	now := time.Now()

	var bottom []string
	editorAt := -1
	switch p := m.top(); {
	case p != nil:
		bottom = m.statusLines(now, nil)
		bottom = append(bottom, strings.Split(p.View(m.width, max(m.height*2/3, 8)), "\n")...)
	case m.page != nil:
		bottom = []string{m.pageFooter()}
	default:
		bottom = m.statusLines(now, m.pending)
		editorAt = len(bottom)
		bottom = append(bottom, strings.Split(m.editor.View(m.width), "\n")...)
		if menu := m.editor.Menu(m.width - 1); menu != nil {
			bottom = append(bottom, menu...)
		} else {
			bottom = append(bottom, m.footer())
		}
	}

	main := m.mainView(max(m.height-len(bottom), 1), now)
	lines := append(main, bottom...)
	// On a short screen the bottom takes it all; the top gives way.
	cut := max(len(lines)-m.height, 0)
	m.mainTop -= cut
	v.SetContent(strings.Join(lines[cut:], "\n"))

	if editorAt >= 0 {
		if c := m.editor.Cursor(); c != nil {
			c.Y += len(main) + editorAt - cut
			v.Cursor = c
		}
	}
	return v
}

// statusLines render what goes on above the input: the run, and a shell
// line running, set off from the conversation by a blank line.
func (m *Model) statusLines(now time.Time, pending []pending) []string {
	lines := m.run.lines(m.width, now, pending)
	if m.shell != nil {
		lines = append([]string{shellLine(m.shell, m.width, now, !m.run.active)}, lines...)
	}
	if len(lines) == 0 {
		return nil
	}
	return append([]string{""}, lines...)
}

// mainView renders the conversation, or the page over it, height lines.
func (m *Model) mainView(height int, now time.Time) []string {
	var head []string
	if m.page != nil {
		head = []string{m.pageHeader(now)}
	}
	v := m.main()
	v.width = max(m.width-2, 10)
	v.height = max(height-len(head), 1)
	v.params = transcript.Params{Expanded: m.expanded, Now: now}
	m.mainHeight = v.height
	m.mainTop = len(head)

	lines := v.view()
	for i, l := range lines {
		lines[i] = " " + l
	}
	for len(lines) < v.height {
		lines = append(lines, "")
	}
	if !v.follow {
		pill := "↓ end to jump to the latest"
		if n := v.unseen(); n > 0 {
			pill = fmt.Sprintf("↓ %d new · end to jump", n)
		}
		pill = lipgloss.NewStyle().Background(theme.Surface).Foreground(theme.Accent).Render(" " + pill + " ")
		lines[len(lines)-1] = strings.Repeat(" ", max(m.width-ansi.StringWidth(pill)-1, 0)) + pill
	}
	return append(head, lines...)
}

func (m *Model) pageHeader(now time.Time) string {
	state := theme.OKText.Render("done")
	if m.page.live() {
		state = theme.WarmText.Render(transcript.Spinner(now) + " running")
	}
	line := " " + theme.Selected.Render(m.page.title) + theme.SubtleText.Render(" · ") + state
	return ansi.Truncate(line, m.width, "…")
}

func (m *Model) pageFooter() string {
	return " " + theme.Hint("esc", "back", "↑↓ wheel", "scroll", "ctrl+o", "expand")
}

// footer shows the permission mode, or a passing message, and the state of
// the conversation.
func (m *Model) footer() string {
	left := lipgloss.NewStyle().Foreground(modeColor(m.mode)).Render("⏵ "+modeLabel(m.mode)) + theme.FaintText.Render("  shift+tab")
	if m.toast != "" {
		left = theme.AccentText.Render(m.toast)
	}

	var right []string
	st := m.status
	model := st.Model
	if st.Effort != "" {
		model += " · " + st.Effort
	}
	right = append(right, theme.MutedText.Render(model))
	if st.Window > 0 {
		pct := st.Context * 100 / st.Window
		c := theme.SubtleText
		switch {
		case pct >= 85:
			c = theme.ErrorText
		case pct >= 60:
			c = theme.WarmText
		}
		right = append(right, c.Render(fmt.Sprintf("ctx %d%%", pct)))
	}
	if st.Worktree != "" {
		right = append(right, theme.WarmText.Render("worktree"))
	}
	if m.branch != "" {
		right = append(right, theme.SubtleText.Render("⎇ "+m.branch))
	}
	if n := len(m.conv.Agents().ActiveAgents()); n > 0 {
		right = append(right, lipgloss.NewStyle().Foreground(theme.Agent).Render(fmt.Sprintf("%d %s · /agents", n, plural(n, "agent"))))
	}
	switch t := m.app.Trust(); {
	case t.Denied || len(t.Agreed) == 0 && len(t.Ask()) > 0:
		right = append(right, theme.WarmText.Render("folder untrusted · /trust"))
	case len(t.Ask()) > 0:
		right = append(right, theme.WarmText.Render(fmt.Sprintf("%d waiting for trust · /trust", len(t.Ask()))))
	}
	r := strings.Join(right, theme.FaintText.Render(" · "))

	room := m.width - 2
	if ansi.StringWidth(left)+ansi.StringWidth(r)+2 > room {
		r = ansi.TruncateLeft(r, ansi.StringWidth(left)+ansi.StringWidth(r)+2-room, "…")
	}
	gap := max(room-ansi.StringWidth(left)-ansi.StringWidth(r), 1)
	return ansi.Truncate(" "+left+strings.Repeat(" ", gap)+r, m.width, "")
}

func modeLabel(m interact.Mode) string {
	switch m {
	case interact.ModeAcceptEdits:
		return "accept edits"
	case interact.ModeTrust:
		return "trust · no prompts"
	}
	return string(m)
}

func modeColor(m interact.Mode) color.Color {
	switch m {
	case interact.ModeStrict:
		return theme.Info
	case interact.ModeAcceptEdits:
		return theme.Warm
	case interact.ModeTrust:
		return theme.Danger
	}
	return theme.Accent
}

func emptyPage(width, height int) []string {
	return []string{theme.SubtleText.Render("Nothing yet")}
}

// renderAll renders cells one under the other, as the conversation shows
// them, with a margin.
func renderAll(cells []transcript.Cell, width int, expanded bool) []string {
	v := newChatView(func() []transcript.Cell { return cells }, emptyPage)
	v.width = max(width-2, 10)
	v.params = transcript.Params{Expanded: expanded, Now: time.Now()}
	var out []string
	for i := range cells {
		for _, l := range v.lines(cells, i) {
			out = append(out, " "+l)
		}
	}
	return out
}

// pager opens the conversation in the user's pager, for searching and
// copying: less shows it in color; another pager gets plain text.
func (m *Model) pager() tea.Cmd {
	lines := renderAll(m.main().cells(), m.width, true)
	args := []string{"less", "-R", "+G"}
	if p := os.Getenv("PAGER"); p != "" {
		args = strings.Fields(p)
		for i, l := range lines {
			lines[i] = ansi.Strip(l)
		}
	}
	f, err := os.CreateTemp("", "codebot-transcript-*.txt")
	if err != nil {
		return emit(transcript.Fail("Could not open the pager: " + err.Error()))
	}
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		return emit(transcript.Fail("Could not open the pager: " + err.Error()))
	}
	cmd := exec.Command(args[0], append(args[1:], f.Name())...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		os.Remove(f.Name())
		if err != nil {
			return transcript.Fail("Pager: " + err.Error())
		}
		return nil
	})
}

func emit(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }
