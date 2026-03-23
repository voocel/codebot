package tui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
	reflowwrap "github.com/muesli/reflow/wrap"
)

// Precomputed grayscale styles for scanText (xterm-256 indices 240..255).
var scanStyles [16]lipgloss.Style

func init() {
	for i := range scanStyles {
		scanStyles[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprintf("%d", 240+i)))
	}
}

// scanText renders text with a scanning light effect.
// speed: how fast the light moves (chars per second).
// band:  number of fully bright characters at center.
// slope: number of gradient characters on each side of the band.
func scanText(text string, now float64, speed float64, band, slope int) string {
	runes := []rune(text)
	n := len(runes)
	if n == 0 {
		return ""
	}

	total := float64(n + band + slope*2)
	center := math.Mod(now*speed, total)

	var b strings.Builder
	for i, r := range runes {
		dist := math.Abs(float64(i) - center)
		halfBand := float64(band) / 2.0

		var brightness float64
		if dist <= halfBand {
			brightness = 1.0
		} else if slope > 0 {
			fade := (dist - halfBand) / float64(slope)
			if fade < 1.0 {
				brightness = 1.0 - fade
			}
		}

		idx := int(brightness * 15)
		b.WriteString(scanStyles[idx].Render(string(r)))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Live area renderers (used by View)
// ---------------------------------------------------------------------------

func (m *Model) renderWelcome() string {
	accent := lipgloss.NewStyle().Foreground(ColorPrimary)
	bold := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	dim := WelcomeDetailStyle

	// ── Left column: centered robot logo ──
	logo := []string{
		"",
		accent.Render("     ╭───╮"),
		accent.Render("     │") + bold.Render("◉ ◉") + accent.Render("│"),
		accent.Render("     ╰─┬─╯"),
		accent.Render("    ╭──┴──╮"),
		accent.Render("    │ ") + bold.Render(">>>") + accent.Render(" │"),
		accent.Render("    ╰─────╯"),
		"",
	}
	const leftWidth = 18 // visible char width of left column (logo area)

	// ── Right column: Tips + separator + Recent activity ──
	rightWidth := 40
	if m.Width > 0 {
		rightWidth = max(m.Width-leftWidth-10, 24) // 10 = border(2) + padding(2) + sep col(1) + gaps(5)
	}

	rightLines := []string{
		"",
		bold.Render("Tips for getting started"),
		dim.Render("  Type / to browse commands"),
		dim.Render("  Enter send · Ctrl+J newline · Esc abort"),
		accent.Render(strings.Repeat("─", rightWidth+2)),
		bold.Render("Recent activity"),
		dim.Render("  No recent activity"),
		"",
	}

	// ── Merge left + right into rows ──
	totalRows := max(len(logo), len(rightLines))
	var rows []string
	for i := range totalRows {
		left := ""
		if i < len(logo) {
			left = logo[i]
		}
		vis := lipgloss.Width(left)
		if vis < leftWidth {
			left += strings.Repeat(" ", leftWidth-vis)
		}

		right := ""
		if i < len(rightLines) {
			right = rightLines[i]
		}
		rows = append(rows, left+" "+accent.Render("│")+" "+right)
	}

	// ── Footer: model info (spans full width) ──
	infoStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	modelLine := infoStyle.Render(m.ModelName)
	if m.Cwd != "" {
		modelLine += dim.Render(" · ") + infoStyle.Render(shortenPath(m.Cwd))
	}
	if m.GitBranch != "" {
		modelLine += dim.Render(" (") + accent.Render(m.GitBranch) + dim.Render(")")
	}

	// ── Custom border with version title ──
	ver := m.Version
	if ver == "" {
		ver = "dev"
	}
	title := " Codebot " + ver + " "

	// Inner width = leftWidth + 1(sep) + 2(gaps) + rightWidth
	innerWidth := leftWidth + 1 + 2 + rightWidth
	// Add 2 for padding (1 char each side)
	totalInner := innerWidth + 2

	// Top border: ╭── title ──...──╮
	titleStyled := bold.Render(title)
	titleVis := lipgloss.Width(titleStyled)
	dashesAfter := totalInner - titleVis
	if dashesAfter < 1 {
		dashesAfter = 1
	}
	topBorder := accent.Render("╭──") + titleStyled + accent.Render(strings.Repeat("─", dashesAfter)+"╮")

	// Body rows with side borders
	var bodyLines []string
	for _, row := range rows {
		vis := lipgloss.Width(row)
		pad := totalInner - vis
		if pad < 0 {
			pad = 0
		}
		bodyLines = append(bodyLines,
			accent.Render("│")+" "+row+strings.Repeat(" ", pad)+" "+accent.Render("│"))
	}

	// Footer row (model info, spans full width)
	footerVis := lipgloss.Width(modelLine)
	footerPad := totalInner - footerVis
	if footerPad < 0 {
		footerPad = 0
	}
	bodyLines = append(bodyLines,
		accent.Render("│")+" "+modelLine+strings.Repeat(" ", footerPad)+" "+accent.Render("│"))

	// Bottom border
	bottomBorder := accent.Render("╰" + strings.Repeat("─", totalInner+2) + "╯")

	result := "\n" + topBorder + "\n" + strings.Join(bodyLines, "\n") + "\n" + bottomBorder
	if m.EnvHint != "" {
		result += "\n" + MutedStyle.Render("  "+m.EnvHint)
	}
	if m.MCPLoading {
		result += "\n" + MutedStyle.Render("  ") + m.ToolSpinner.View() + MutedStyle.Render(" MCP servers connecting...")
	}
	return result
}

// RenderStatusBar renders the status line above the input.
// Only shown while the agent is running.
func (m *Model) RenderStatusBar() string {
	if m.Permission != nil || m.AskUser != nil {
		return ""
	}
	if m.config.StatusPlan != nil {
		if plan := m.config.StatusPlan(m); plan != nil && len(plan.Choices) > 0 {
			return ""
		}
	}
	if !m.Running {
		return ""
	}

	elapsed := time.Since(m.RunStats.StartedAt).Truncate(time.Second)
	now := float64(time.Now().UnixMilli()) / 1000.0
	status := m.Spinner.View() + " " + scanText("Running...", now, 20.0, 2, 2)
	status += "  " + MutedStyle.Render(fmt.Sprintf("(%s · ↑ %s ↓ %s tokens)",
		formatDuration(elapsed), FormatTokens(m.RunStats.DisplayInput), FormatTokens(m.RunStats.DisplayOutput)))

	if m.Width > 0 {
		status = truncate.StringWithTail(status, uint(max(m.Width-2, 1)), "…")
	}
	return status
}

// RenderPlanBar renders the plan review card (AskUser-style). Empty when inactive.
func (m *Model) RenderPlanBar() string {
	if m.config.StatusPlan == nil {
		return ""
	}
	plan := m.config.StatusPlan(m)
	if plan == nil || len(plan.Choices) == 0 {
		return ""
	}

	optionCount := len(plan.Choices) + 1 // +1 for "Type here"

	var b strings.Builder

	// Prompt text.
	if plan.Prompt != "" {
		b.WriteString(askQuestionStyle.Render(plan.Prompt))
		b.WriteString("\n\n")
	}

	// Numbered options.
	for i, c := range plan.Choices {
		num := fmt.Sprintf("%d. ", i+1)
		if i == plan.Active {
			b.WriteString(askOptionActiveStyle.Render("> " + num + c))
		} else {
			b.WriteString(askOptionInactiveStyle.Render("  " + num + c))
		}
		b.WriteByte('\n')
	}

	// Separator before "Type here".
	b.WriteString(askDescStyle.Render("  ───"))
	b.WriteByte('\n')

	// "Type here" option.
	otherIdx := len(plan.Choices)
	otherNum := fmt.Sprintf("%d. ", otherIdx+1)
	if plan.Active == otherIdx {
		if plan.OtherMode {
			b.WriteString(askOptionActiveStyle.Render("> " + otherNum + plan.OtherBuf + "█"))
		} else {
			b.WriteString(askOptionActiveStyle.Render("> " + otherNum + "Type here to tell Claude what to change"))
		}
	} else {
		b.WriteString(askOptionInactiveStyle.Render("  " + otherNum + "Type here to tell Claude what to change"))
	}
	b.WriteString("\n\n")

	// Hint line.
	if plan.OtherMode {
		b.WriteString(askHintStyle.Render("Enter to confirm · Esc to go back"))
	} else {
		b.WriteString(askHintStyle.Render(fmt.Sprintf("Enter to select · ↑↓ Navigate · 1-%d Shortcut · Esc to cancel", optionCount)))
	}

	return indentBlock(b.String(), 2)
}

// RenderContextBar renders the context line below the input (env info).
func (m *Model) RenderContextBar() string {
	if m.QuitPending {
		return lipgloss.NewStyle().Foreground(ColorMuted).Bold(true).Render("Press Ctrl+C again to exit")
	}
	if strings.HasPrefix(m.Input.Value(), "!") {
		return ShellSeparatorStyle.Render("! for bash mode")
	}
	var parts []string
	if m.config.StatusMode != nil {
		if mode := m.config.StatusMode(m); mode != "" {
			parts = append(parts, lipgloss.NewStyle().Foreground(ColorPrimary).Render(mode))
		}
	}
	if m.Cwd != "" {
		parts = append(parts, filepath.Base(m.Cwd))
	}
	parts = append(parts, m.ModelName)
	if m.config.StatusRight != nil {
		if extra := m.config.StatusRight(m); extra != "" {
			parts = append(parts, extra)
		}
	}
	line := MutedStyle.Render(strings.Join(parts, " · "))
	if m.Width > 0 {
		line = truncate.StringWithTail(line, uint(max(m.Width-2, 1)), "…")
	}
	return line
}

func (m Model) renderCommandPalette() string {
	if len(m.compItems) == 0 {
		return ""
	}
	selected := m.compItems[min(max(m.compIdx, 0), len(m.compItems)-1)]

	width := 74
	if m.Width > 0 {
		width = min(max(m.Width-2, 34), 92)
	}

	list, remaining := m.renderCommandPaletteList(width)
	var lines []string
	lines = append(lines, CommandPaletteTitleStyle.Render("Commands")+"  "+CommandPaletteHintStyle.Render("/"+selected.Name))
	lines = append(lines, list)
	lines = append(lines, renderCommandPaletteFooter(selected, remaining, width))
	lines = append(lines, "")

	hint := "↑↓ move · Tab complete · Enter run/fill · Esc close"
	content := strings.Join(append(lines, CommandPaletteHintStyle.Render(hint)), "\n")
	box := CommandPaletteStyle.Width(width).Render(content)

	// Pad below the box to keep total height stable (prevents input jumping).
	visibleRows := min(len(m.compItems), commandPaletteMaxVisible)
	padLines := commandPaletteMaxVisible - visibleRows
	if padLines > 0 {
		box += strings.Repeat("\n", padLines)
	}
	return box
}

const commandPaletteMaxVisible = 8

func (m Model) renderCommandPaletteList(width int) (string, int) {
	start, end := commandPaletteWindow(len(m.compItems), m.compIdx, commandPaletteMaxVisible)
	lines := make([]string, 0, commandPaletteMaxVisible)

	for i := start; i < end; i++ {
		lines = append(lines, renderCommandPaletteRow(m.compItems[i], width, i == m.compIdx))
	}

	return strings.Join(lines, "\n"), len(m.compItems) - end
}

func renderCommandPaletteRow(item CompletionItem, width int, selected bool) string {
	marker := " "
	if selected {
		marker = "›"
	}

	nameWidth := min(max(width/3, 12), 18)
	name := truncate.StringWithTail("/"+item.Name, uint(nameWidth), "…")
	// Pad name to fixed column width using display width.
	nameText := name + strings.Repeat(" ", max(nameWidth-lipgloss.Width(name), 0))
	// Truncate description by display width to prevent line wrapping.
	descMaxWidth := max(width-nameWidth-4, 10) // 4 = marker(1) + space(1) + gap(1) + margin(1)
	desc := truncateByWidth(item.Description, descMaxWidth)
	prefix := marker + " "
	if selected {
		return CommandPaletteSelectedStyle.Render(prefix) + CommandPaletteSelectedStyle.Render(nameText) + " " + CommandPaletteSelectedDescStyle.Render(desc)
	}
	return prefix + CommandPaletteItemStyle.Render(nameText) + " " + CommandPaletteDescStyle.Render(desc)
}

// truncateByWidth truncates s to fit within maxWidth display columns.
func truncateByWidth(s string, maxWidth int) string {
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	tail := "…"
	limit := maxWidth - lipgloss.Width(tail)
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > limit {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteString(tail)
	return b.String()
}

func renderCommandPaletteFooter(item CompletionItem, remaining, width int) string {
	nameWidth := min(max(width/3, 12), 18)
	descStart := 2 + nameWidth + 1

	left := "  "
	if remaining > 0 {
		left = "  " + fmt.Sprintf("… %d more commands", remaining)
	}
	usage := "Usage: " + item.Usage
	if len(item.Aliases) > 0 {
		var aliases []string
		for _, alias := range item.Aliases {
			aliases = append(aliases, "/"+alias)
		}
		usage += " · Aliases: " + strings.Join(aliases, ", ")
	}

	if lipgloss.Width(left) >= descStart {
		return CommandPaletteHintStyle.Render(left) + "  " + MutedStyle.Render(usage)
	}
	padding := strings.Repeat(" ", descStart-lipgloss.Width(left))
	return CommandPaletteHintStyle.Render(left) + padding + MutedStyle.Render(usage)
}

func commandPaletteWindow(total, cursor, limit int) (start, end int) {
	if total <= limit {
		return 0, total
	}
	start = max(cursor-limit/2, 0)
	end = min(start+limit, total)
	if end-start < limit {
		start = max(end-limit, 0)
	}
	return start, end
}

// ---------------------------------------------------------------------------
// Markdown rendering
// ---------------------------------------------------------------------------

// RenderMarkdown renders markdown content using glamour.
func (m *Model) RenderMarkdown(content string) string {
	if m.Markdown == nil || content == "" {
		return content
	}
	return m.Markdown.RenderFinal(content)
}

// renderMarkdownBlock renders complete markdown and applies only outer
// indentation. Markdown output is already wrapped by the renderer.
func (m Model) renderMarkdownBlock(content string, indent int) string {
	if content == "" {
		return ""
	}
	if m.Markdown == nil {
		return indentBlock(m.wrapTextForIndent(content, indent), indent)
	}
	return indentBlock(m.RenderMarkdown(content), indent)
}

// ---------------------------------------------------------------------------
// Run summary
// ---------------------------------------------------------------------------

// renderRunSummary renders per-run stats shown after agent completion.
func (m *Model) renderRunSummary() string {
	s := m.RunStats
	return MutedStyle.Render(fmt.Sprintf("─ %d turns · %d tools · ↑%s ↓%s tokens · %s",
		s.Turns, s.ToolCalls, FormatTokens(s.Input), FormatTokens(s.Output), formatDuration(s.Duration)))
}

// renderQueuedMsgs renders queued messages sent while agent is running.
func (m *Model) renderQueuedMsgs() string {
	var b strings.Builder
	for _, msg := range m.QueuedMsgs {
		text := truncateRunes(msg, 80)
		b.WriteString(QueuedMsgStyle.Render("  ↳ " + text))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderTaskList renders the task progress bar and task list.
func (m *Model) renderTaskList() string {
	snap := m.Tasks
	if snap == nil || snap.Total == 0 {
		return ""
	}

	var b strings.Builder

	// Progress bar: [████░░░░] 2/5 completed
	barWidth := min(snap.Total, 20)
	filled := 0
	if snap.Total > 0 {
		filled = snap.Completed * barWidth / snap.Total
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	b.WriteString(MutedStyle.Render(fmt.Sprintf("[%s] %d/%d completed", bar, snap.Completed, snap.Total)))

	// One line per task.
	for _, t := range snap.Tasks {
		b.WriteByte('\n')
		var icon string
		var color lipgloss.TerminalColor
		switch t.Status {
		case "pending":
			icon = "○"
			color = ColorMuted
		case "in_progress":
			icon = "◐"
			color = ColorTool
		case "completed":
			icon = "●"
			color = ColorSuccess
		default:
			icon = "○"
			color = ColorMuted
		}
		text := t.Subject
		if t.Status == "in_progress" && t.ActiveForm != "" {
			text = t.ActiveForm
		}
		style := lipgloss.NewStyle().Foreground(color)
		b.WriteString(style.Render(fmt.Sprintf("%s #%s %s", icon, t.ID, text)))
	}

	return indentBlock(b.String(), 2)
}

// FormatTokens formats a token count with k/M suffix for readability.
func FormatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// ---------------------------------------------------------------------------
// User message rendering
// ---------------------------------------------------------------------------

// renderUserMessage renders a sent user message with the textarea's background.
//
// IMPORTANT: lipgloss Render() appends ANSI reset (\033[0m) after each call.
// If you nest styled content inside an outer Background style, the inner reset
// kills the outer background — causing "text has color A, padding has color B".
// To avoid this, every styled segment (icon, text, padding) must carry its own
// Background. This way each segment is self-contained and resets between them
// are invisible (zero characters wide).
func (m *Model) renderUserMessage(text string) string {
	bg := m.Input.FocusedStyle.CursorLine.GetBackground()
	iconStyle := lipgloss.NewStyle().Foreground(ColorMuted).Background(bg)
	textStyle := lipgloss.NewStyle().Foreground(ColorUser).Background(bg)
	padStyle := lipgloss.NewStyle().Background(bg)

	wrapped := m.wrapTextForIndent(text, 2)
	lines := strings.Split(wrapped, "\n")

	var sb strings.Builder
	for i, line := range lines {
		var rendered string
		if i == 0 {
			rendered = iconStyle.Render("❯ ") + textStyle.Render(line)
		} else {
			rendered = textStyle.Render("  " + line)
		}
		// Pad remaining width so background fills the full terminal line.
		if pad := m.Width - lipgloss.Width(rendered); pad > 0 {
			rendered += padStyle.Render(strings.Repeat(" ", pad))
		}
		sb.WriteString(rendered)
		if i < len(lines)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Text utilities
// ---------------------------------------------------------------------------

// wrapTextForIndent wraps content to fit terminal width after indentation.
func (m Model) wrapTextForIndent(content string, indent int) string {
	if content == "" {
		return ""
	}
	width := m.Width - indent - 1
	if width <= 1 {
		width = 79
	}
	return strings.TrimRight(reflowwrap.String(content, width), "\n")
}

// dedent strips the common leading whitespace from all lines.
// Preserves relative indentation (code blocks, lists) while removing
// any unwanted base indentation added by renderers like glamour.
func dedent(s string) string {
	lines := strings.Split(s, "\n")
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		return s
	}
	for i, line := range lines {
		if len(line) >= minIndent {
			lines[i] = line[minIndent:]
		}
	}
	return strings.Join(lines, "\n")
}

// indentBlock prepends n spaces to each non-empty line.
func indentBlock(s string, n int) string {
	if s == "" {
		return ""
	}
	prefix := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

// shortenPath replaces the home directory prefix with ~.
func shortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// formatDuration formats a duration as "Xs", "Xm Xs", or "Xh Xm".
func formatDuration(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, m)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-3]) + "..."
}

// ---------------------------------------------------------------------------
// Box drawing
// ---------------------------------------------------------------------------

// DrawBox draws a rounded border box with fixed height and gray border.
// innerWidth is the content width; fixedRows is the exact number of content rows
// (short content is padded with empty lines to keep view height stable).
func DrawBox(lines []string, innerWidth, fixedRows int) string {
	bs := BoxBorderStyle
	padWidth := innerWidth + 2 // 1 padding each side

	emptyLine := bs.Render("│") + " " + strings.Repeat(" ", innerWidth) + " " + bs.Render("│")

	var b strings.Builder
	b.WriteString(bs.Render("╭" + strings.Repeat("─", padWidth) + "╮"))
	b.WriteByte('\n')

	for i := range fixedRows {
		if i < len(lines) {
			line := lines[i]
			vis := lipgloss.Width(line)
			if vis > innerWidth {
				line = truncateToWidth(line, innerWidth)
				vis = lipgloss.Width(line)
			}
			pad := max(innerWidth-vis, 0)
			b.WriteString(bs.Render("│") + " " + line + strings.Repeat(" ", pad) + " " + bs.Render("│"))
		} else {
			b.WriteString(emptyLine)
		}
		b.WriteByte('\n')
	}

	b.WriteString(bs.Render("╰" + strings.Repeat("─", padWidth) + "╯"))
	return b.String()
}

// truncateToWidth truncates a string to fit within maxWidth visual cells.
func truncateToWidth(s string, maxWidth int) string {
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > maxWidth {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}
