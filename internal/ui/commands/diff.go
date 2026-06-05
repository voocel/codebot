package commands

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/ui/tui"
)

// DiffCommand drives /diff — an interactive overlay listing the files the last
// turn changed (what /undo would roll back), with per-file added/removed counts.
type DiffCommand struct {
	session *agent.Session
	overlay OverlayController
	state   *diffListState
}

type diffStatEntry struct {
	path     string
	add, del int
	binary   bool
}

type diffListState struct {
	files          []diffStatEntry
	cursor         int
	totAdd, totDel int
}

// Diff constructs the /diff command.
func Diff(session *agent.Session, overlay OverlayController) *DiffCommand {
	return &DiffCommand{session: session, overlay: overlay}
}

func (c *DiffCommand) Spec() Spec {
	return Spec{
		Name:        "diff",
		Usage:       "/diff",
		Description: "Preview the file changes /undo would roll back",
		Category:    "session",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}
}

func (c *DiffCommand) Run(_ Invocation) tea.Cmd {
	if notice := snapshotUnavailable(c.session); notice != nil {
		return notice
	}
	numstat, err := c.session.Diff()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Diff failed: " + err.Error()))
	}
	files, totAdd, totDel := parseNumstat(numstat)
	if len(files) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render(
			"Nothing to preview — no file changes to undo."))
	}
	c.state = &diffListState{files: files, totAdd: totAdd, totDel: totDel}
	c.overlay.SetOverlay(c)
	return nil
}

func (c *DiffCommand) Active() bool  { return c.state != nil }
func (c *DiffCommand) IsModal() bool { return true }
func (c *DiffCommand) Dismiss()      { c.state = nil }

func (c *DiffCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if c.state == nil {
		return false, nil
	}
	switch msg.String() {
	case "up", "k":
		if c.state.cursor > 0 {
			c.state.cursor--
		}
		return true, nil
	case "down", "j":
		if c.state.cursor < len(c.state.files)-1 {
			c.state.cursor++
		}
		return true, nil
	case "esc", "ctrl+c", "q", "enter":
		// MVP: no detail view yet, so enter just closes like esc.
		c.overlay.ClearOverlay()
		return true, nil
	}
	return true, nil
}

// parseNumstat turns git --numstat output into per-file entries plus line
// totals. A binary file reports "-\t-" and contributes nothing to the totals.
func parseNumstat(numstat string) (files []diffStatEntry, totAdd, totDel int) {
	for line := range strings.SplitSeq(strings.TrimSpace(numstat), "\n") {
		// numstat line: "<adds>\t<dels>\t<path>"; binary files report "-".
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		e := diffStatEntry{path: fields[2]}
		if fields[0] == "-" || fields[1] == "-" {
			e.binary = true
		} else {
			e.add, _ = strconv.Atoi(fields[0])
			e.del, _ = strconv.Atoi(fields[1])
			totAdd += e.add
			totDel += e.del
		}
		files = append(files, e)
	}
	return files, totAdd, totDel
}

// diffSelectedRowStyle paints the selected file row as a filled highlight band
// (light fg on brand bg), matching codebot's active-tab convention.
var diffSelectedRowStyle = lipgloss.NewStyle().Foreground(tui.Strong).Background(tui.Brand).Bold(true)

func (c *DiffCommand) View(width, height int) string {
	if c.state == nil {
		return ""
	}
	s := c.state

	var sb strings.Builder

	// Top border.
	sb.WriteString(diffRule(width))
	sb.WriteString("\n\n")

	// Title + summary ("N files changed +X -Y"; a zero side is omitted).
	sb.WriteString(tui.CardTitleStyle.Render("Changes /undo will revert"))
	sb.WriteByte('\n')
	noun := "files"
	if len(s.files) == 1 {
		noun = "file"
	}
	_, summary := diffCounts(diffStatEntry{add: s.totAdd, del: s.totDel})
	fmt.Fprintf(&sb, "%d %s changed %s\n\n", len(s.files), noun, summary)

	// Rows that fit, reserving the borders + title + summary + hint chrome.
	limit := len(s.files)
	if height > 0 {
		if avail := height - 9; avail >= 1 && avail < limit {
			limit = avail
		}
	}
	for i := 0; i < limit; i++ {
		sb.WriteString(c.renderRow(s.files[i], i == s.cursor, width))
		sb.WriteByte('\n')
	}
	if limit < len(s.files) {
		fmt.Fprintf(&sb, "  %s\n", tui.MutedStyle.Render(fmt.Sprintf("↓ %d more", len(s.files)-limit)))
	}

	sb.WriteString("\n")
	sb.WriteString(tui.MutedStyle.Italic(true).Render("  ↑/↓ select · Esc close"))
	sb.WriteString("\n\n")

	// Bottom border.
	sb.WriteString(diffRule(width))
	return sb.String()
}

// diffRule renders a full-width horizontal divider for the overlay's top and
// bottom borders, reusing codebot's standard overlay separator color.
func diffRule(width int) string {
	if width < 1 {
		width = 80
	}
	return tui.SeparatorStyle.Render(strings.Repeat("─", width))
}

// renderRow lays out one file row: "❯ name ........ +N -M", the name in the
// terminal's default color (highlighted when selected), counts right-aligned
// and colored (green additions, red deletions).
func (c *DiffCommand) renderRow(e diffStatEntry, selected bool, width int) string {
	prefix := "  "
	if selected {
		prefix = "❯ "
	}
	numPlain, numColored := diffCounts(e)

	const rightMargin = 1
	// Truncate an over-long name, keeping the tail (prefixed with …).
	nameBudget := width - lipgloss.Width(prefix) - lipgloss.Width(numPlain) - rightMargin - 1
	name := e.path
	if nameBudget > 1 {
		if runes := []rune(name); len(runes) > nameBudget {
			name = "…" + string(runes[len(runes)-nameBudget+1:])
		}
	}

	left := prefix + name
	leftWidth := lipgloss.Width(left)
	if selected {
		left = diffSelectedRowStyle.Render(left)
	}

	pad := max(1, width-leftWidth-lipgloss.Width(numPlain)-rightMargin)
	return left + strings.Repeat(" ", pad) + numColored
}

// diffCounts returns the per-file change segment both as plain text (for width
// math) and colored (green +adds / red -dels). A zero side is omitted; binary
// is "Bin".
func diffCounts(e diffStatEntry) (plain, colored string) {
	if e.binary {
		return "Bin", tui.MutedStyle.Render("Bin")
	}
	var p, col []string
	if e.add > 0 {
		p = append(p, fmt.Sprintf("+%d", e.add))
		col = append(col, tui.DiffStatAddStyle.Render(fmt.Sprintf("+%d", e.add)))
	}
	if e.del > 0 {
		p = append(p, fmt.Sprintf("-%d", e.del))
		col = append(col, tui.DiffStatRemoveStyle.Render(fmt.Sprintf("-%d", e.del)))
	}
	return strings.Join(p, " "), strings.Join(col, " ")
}
