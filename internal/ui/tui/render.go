package tui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
	reflowwrap "github.com/muesli/reflow/wrap"
	"github.com/voocel/codebot/internal/tools"
)

var (
	scanStepsLight = [...]string{"243", "243", "244", "244", "245", "245", "246", "246", "247", "247", "248", "248", "249", "250", "251", "252"}
	scanStepsDark  = [...]string{"246", "246", "247", "248", "248", "249", "250", "250", "251", "252", "252", "253", "254", "255", "255", "255"}
)

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
		b.WriteString(lipgloss.NewStyle().Foreground(scanStepColor(idx)).Render(string(r)))
	}
	return b.String()
}

func scanStepColor(idx int) lipgloss.TerminalColor {
	if idx < 0 {
		idx = 0
	}
	if idx > 15 {
		idx = 15
	}
	if lipgloss.HasDarkBackground() {
		return lipgloss.Color(scanStepsDark[idx])
	}
	return lipgloss.Color(scanStepsLight[idx])
}

// ---------------------------------------------------------------------------
// Live area renderers (used by View)
// ---------------------------------------------------------------------------

func (m *Model) renderWelcome() string {
	width := 84
	if m.Width > 0 {
		width = min(max(m.Width-4, 56), 96)
	}

	ver := m.Version
	if ver == "" {
		ver = "dev"
	}

	bc := lipgloss.NewStyle().Foreground(ColorPrimarySoft)
	edge := WelcomeKickerStyle
	title := WelcomeTitleStyle

	// Terminal window logo with drop shadow
	dotR := lipgloss.NewStyle().Foreground(ColorError)
	dotY := lipgloss.NewStyle().Foreground(ColorTool)
	dotG := lipgloss.NewStyle().Foreground(ColorSuccess)
	cursor := lipgloss.NewStyle().Foreground(ColorAccent)
	shadow := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "249", Dark: "238"})

	logoLines := []string{
		edge.Render("╭──────────────────────╮"),
		edge.Render("│") + " " + dotR.Render("●") + " " + dotY.Render("●") + " " + dotG.Render("●") + "    " + title.Render("CODEBOT") + "     " + edge.Render("│"),
		edge.Render("├──────────────────────┤") + shadow.Render("░"),
		edge.Render("│") + "                      " + edge.Render("│") + shadow.Render("░"),
		edge.Render("│") + "       " + edge.Render("❯_") + " " + cursor.Render("█") + "           " + edge.Render("│") + shadow.Render("░"),
		edge.Render("│") + "                      " + edge.Render("│") + shadow.Render("░"),
		edge.Render("╰──────────────────────╯") + shadow.Render("░"),
		" " + shadow.Render(strings.Repeat("░", 23)),
	}
	logo := lipgloss.NewStyle().Width(27).Render(strings.Join(logoLines, "\n"))

	// Vertical separator (match logo height)
	const logoHeight = 8
	sepParts := make([]string, logoHeight)
	for i := range sepParts {
		sepParts[i] = bc.Render("│")
	}
	sep := strings.Join(sepParts, "\n")

	// Right panel
	rightWidth := max(width-34, 20)
	rightLines := []string{
		WelcomeTitleStyle.Render("Long-running coding agent for the terminal."),
		"",
		WelcomeBodyStyle.Render("Small execution kernel. Strong runtime harness."),
		bc.Render(strings.Repeat("─", min(rightWidth, 44))),
		WelcomeMutedStyle.Render("/ commands · Enter send · Esc abort"),
	}
	right := lipgloss.NewStyle().Width(rightWidth).Render(strings.Join(rightLines, "\n"))

	body := lipgloss.JoinHorizontal(lipgloss.Top, logo, " ", sep, " ", right)

	// Footer: provider · model · path · branch (version in top border)
	var footerBits []string
	if m.Provider != "" {
		footerBits = append(footerBits, ContextChipAccentStyle.Render(m.Provider))
	}
	if m.ModelName != "" {
		footerBits = append(footerBits, ContextChipStyle.Render(m.ModelName))
	}
	if m.Cwd != "" {
		footerBits = append(footerBits, ContextChipStyle.Render(shortenPath(m.Cwd)))
	}
	if m.GitBranch != "" {
		footerBits = append(footerBits, ContextChipStyle.Render("branch "+m.GitBranch))
	}
	footer := strings.Join(footerBits, ContextChipStyle.Render(" · "))

	// Assemble inner content
	content := strings.Join([]string{"", body, "", footer}, "\n")

	// Manual frame with title embedded in top border
	innerW := width - 4
	titleTag := WelcomeKickerStyle.Render("Codebot") + " " + ContextChipAccentStyle.Render(ver)
	titleW := lipgloss.Width(titleTag)
	topDash := max(width-titleW-5, 1)
	topLine := bc.Render("╭─ ") + titleTag + " " + bc.Render(strings.Repeat("─", topDash)+"╮")
	botLine := bc.Render("╰" + strings.Repeat("─", width-2) + "╯")

	lines := strings.Split(content, "\n")
	framed := make([]string, 0, len(lines)+2)
	framed = append(framed, topLine)
	for _, line := range lines {
		pad := max(innerW-lipgloss.Width(line), 0)
		framed = append(framed, bc.Render("│ ")+line+strings.Repeat(" ", pad)+bc.Render(" │"))
	}
	framed = append(framed, botLine)

	result := "\n" + strings.Join(framed, "\n")
	if m.EnvHint != "" {
		result += "\n" + InputHintStyle.Render("  "+m.EnvHint)
	}
	if m.MCPLoading {
		result += "\n" + InputHintStyle.Render("  ") + m.ToolSpinner.View() + InputHintStyle.Render(" MCP servers connecting...")
	}
	return result
}

func (m *Model) renderInputPanel() string {
	width := m.Width
	if width <= 0 {
		width = 80
	}

	var sections []string
	if len(m.Images) > 0 {
		var tags []string
		for i := range m.Images {
			tag := fmt.Sprintf("Image #%d", i+1)
			if m.ImageCursor == i {
				tags = append(tags, ImageSelectedStyle.Render("["+tag+"]"))
			} else {
				tags = append(tags, ImageTagStyle.Render(tag))
			}
		}
		line := strings.Join(tags, " ")
		if m.ImageCursor >= 0 {
			line += " " + InputHintStyle.Render("Delete remove · Esc cancel")
		} else {
			line += " " + InputHintStyle.Render("↑ select")
		}
		sections = append(sections, line)
	}

	inputView := m.styledInputView()
	sections = append(sections, inputView)

	content := strings.Join(sections, "\n\n")
	return InputPanelStyle.Width(max(width-2, 20)).Render(content)
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
	for _, detail := range plan.Details {
		detail = strings.TrimSpace(detail)
		if detail == "" {
			continue
		}
		b.WriteString(askDescStyle.Render(detail))
		b.WriteByte('\n')
	}
	if len(plan.Details) > 0 {
		b.WriteByte('\n')
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

	return AskCardStyle.Render(b.String())
}

// RenderContextBar renders the context line below the input (env info).
func (m *Model) RenderContextBar() string {
	if m.QuitPending {
		return ContextChipWarnStyle.Render("Press Ctrl+C again to exit")
	}
	if strings.HasPrefix(m.Input.Value(), "!") {
		return ContextChipWarnStyle.Render("bash mode")
	}
	var chips []string
	if m.config.StatusMode != nil {
		if mode := m.config.StatusMode(m); mode != "" {
			chips = append(chips, ContextChipAccentStyle.Render(mode))
		}
	}
	if m.Cwd != "" {
		chips = append(chips, ContextChipStyle.Render(filepath.Base(m.Cwd)))
	}
	chips = append(chips, ContextChipStyle.Render("· "+m.ModelName))
	if m.config.StatusRight != nil {
		if extra := m.config.StatusRight(m); extra != "" {
			chips = append(chips, ContextChipStyle.Render("· "+extra))
		}
	}
	line := strings.Join(chips, " ")
	if m.Width > 0 {
		line = truncate.StringWithTail(line, uint(max(m.Width-2, 1)), "…")
	}
	return line
}

func (m *Model) renderCommandPalette() string {
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
	header := CommandPaletteTitleStyle.Render("Commands") + "  " +
		CommandPaletteKindBadge(selected.Kind) + " " +
		CommandPaletteCategoryBadge(selected.Category)
	if badge := CommandPaletteIdleBadge(selected.NeedsIdle); badge != "" {
		header += " " + badge
	}
	lines = append(lines, header)
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

func (m *Model) renderCommandPaletteList(width int) (string, int) {
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
	// 6 = marker(1) + space(1) + gap(1) + padding(2) + safety(1)
	descMaxWidth := max(width-nameWidth-6, 10)
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
	contentWidth := max(width-2, 20) // account for padding

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

	usageStart := descStart
	if lipgloss.Width(left) >= descStart {
		usageStart = lipgloss.Width(left) + 2
	}
	usageMax := max(contentWidth-usageStart, 10)
	usage = truncateByWidth(usage, usageMax)

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

// RenderMarkdown renders a lightweight terminal-friendly markdown subset.
func (m *Model) RenderMarkdown(content string) string {
	if m.Markdown == nil || content == "" {
		return content
	}
	return m.Markdown.RenderFinal(content)
}

// renderMarkdownBlock renders complete markdown and applies only outer
// indentation. Markdown output is already wrapped by the renderer.
func (m *Model) renderMarkdownBlock(content string, indent int) string {
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
	style := MutedStyle
	return strings.Join([]string{
		style.Render(fmt.Sprintf("%d turns", s.Turns)),
		style.Render("· " + fmt.Sprintf("%d tools", s.ToolCalls)),
		style.Render("· ↑" + FormatTokens(s.Input)),
		style.Render("· ↓" + FormatTokens(s.Output)),
		style.Render("· " + formatDuration(s.Duration)),
	}, " ")
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

	barWidth := min(max(snap.Total, 10), 24)
	filled := 0
	if snap.Total > 0 {
		filled = snap.Completed * barWidth / snap.Total
	}
	bar := lipgloss.NewStyle().Foreground(ColorPrimary).Render(strings.Repeat("█", filled)) +
		MutedStyle.Render(strings.Repeat("░", barWidth-filled))
	b.WriteString(CardTitleStyle.Render("Task Progress"))
	b.WriteString("\n")
	b.WriteString(TaskProgressStyle.Render(fmt.Sprintf("%s  %d/%d completed", bar, snap.Completed, snap.Total)))
	b.WriteString("\n")
	b.WriteString(MutedStyle.Render(fmt.Sprintf("%d in progress · %d pending", snap.InProgress, snap.Pending)))

	for _, t := range snap.Items {
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
		line := style.Render(fmt.Sprintf("%s #%s", icon, t.ID)) + " " + lipgloss.NewStyle().Foreground(ColorSoftText).Render(text)
		if t.Owner != "" {
			line += " " + TagSubtleStyle.Render("· "+t.Owner)
		}
		if blockers := openTaskBlockers(*snap, t); len(blockers) > 0 {
			line += " " + MutedStyle.Render("blocked by "+strings.Join(blockers, ", "))
		}
		b.WriteString(line)
	}

	return TaskCardStyle.Width(max(min(m.Width-2, 96), 24)).Render(b.String())
}

func openTaskBlockers(snap tools.TaskSnapshot, task tools.Task) []string {
	if len(task.BlockedBy) == 0 {
		return nil
	}
	statusByID := make(map[string]tools.TaskStatus, len(snap.Items))
	for _, item := range snap.Items {
		statusByID[item.ID] = item.Status
	}
	var active []string
	for _, id := range task.BlockedBy {
		if statusByID[id] != tools.TaskCompleted {
			active = append(active, "#"+id)
		}
	}
	sort.Strings(active)
	return active
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
	wrapped := m.wrapTextForIndent(text, 2)
	lines := strings.Split(wrapped, "\n")
	bgStyle := lipgloss.NewStyle().Background(ColorStatusBg)
	prefixStyle := lipgloss.NewStyle().Foreground(ColorUser).Background(ColorStatusBg).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(ColorUser).Background(ColorStatusBg).Bold(true)
	padStyle := lipgloss.NewStyle().Background(ColorStatusBg)
	var out []string
	for i, line := range lines {
		prefix := "  "
		if i == 0 {
			prefix = prefixStyle.Render("❯") + bgStyle.Render(" ")
		}
		rendered := prefix + textStyle.Render(line)
		if pad := m.Width - lipgloss.Width(rendered); pad > 0 {
			rendered += padStyle.Render(strings.Repeat(" ", pad))
		}
		out = append(out, rendered)
	}
	return strings.Join(out, "\n")
}

// ---------------------------------------------------------------------------
// Text utilities
// ---------------------------------------------------------------------------

// wrapTextForIndent wraps content to fit terminal width after indentation.
func (m *Model) wrapTextForIndent(content string, indent int) string {
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
// any unwanted base indentation added by multiline renderers.
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
