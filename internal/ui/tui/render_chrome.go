package tui

// UI chrome around the input area: status/plan/context bars above and below,
// slash-command palette, per-run summary, queued messages, task progress card.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
	"github.com/voocel/codebot/internal/storage"
)

// ---------------------------------------------------------------------------
// Status / plan / context bars
// ---------------------------------------------------------------------------

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
	chips = append(chips, ContextChipStyle.Render("· "+m.formatModelChip()))
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

func (m *Model) formatModelChip() string {
	s := m.ModelName
	if m.ContextWindow > 0 {
		s += " (" + FormatTokens(m.ContextWindow) + ")"
	}
	return s
}

// ---------------------------------------------------------------------------
// Slash-command palette
// ---------------------------------------------------------------------------

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
	visibleRows := min(len(m.compItems), PaletteMaxVisible)
	padLines := PaletteMaxVisible - visibleRows
	if padLines > 0 {
		box += strings.Repeat("\n", padLines)
	}
	return box
}

func (m *Model) renderCommandPaletteList(width int) (string, int) {
	start, end := commandPaletteWindow(len(m.compItems), m.compIdx, PaletteMaxVisible)
	lines := make([]string, 0, PaletteMaxVisible)

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
// Run summary / queued messages / task list
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

func openTaskBlockers(snap storage.TaskSnapshot, task storage.Task) []string {
	if len(task.BlockedBy) == 0 {
		return nil
	}
	statusByID := make(map[string]storage.TaskStatus, len(snap.Items))
	for _, item := range snap.Items {
		statusByID[item.ID] = item.Status
	}
	var active []string
	for _, id := range task.BlockedBy {
		if statusByID[id] != storage.TaskCompleted {
			active = append(active, "#"+id)
		}
	}
	sort.Strings(active)
	return active
}
