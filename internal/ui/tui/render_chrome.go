package tui

// UI chrome around the input area: status/plan/context bars above and below,
// slash-command palette, per-run summary, queued messages, task progress card.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
	"github.com/voocel/codebot/internal/storage"
)

// ---------------------------------------------------------------------------
// Status / plan / context bars
// ---------------------------------------------------------------------------

// RenderStatusBar renders the live status block pinned above the input:
// the Running spinner line (when the agent is active) plus a compact task
// tree (when there are tasks). Either component may be empty depending on
// state — the task tree stays visible between turns so users can track
// progress at idle without losing the momentum view.
func (m *Model) RenderStatusBar() string {
	if m.Permission != nil || m.AskUser != nil {
		return ""
	}
	if m.config.StatusPlan != nil {
		if plan := m.config.StatusPlan(m); plan != nil && len(plan.Choices) > 0 {
			return ""
		}
	}

	var runningLine string
	if m.Running {
		elapsed := time.Since(m.RunStats.StartedAt).Truncate(time.Second)
		now := float64(time.Now().UnixMilli()) / 1000.0
		runningLine = m.Spinner.View() + " " + scanText("Running...", now, 20.0, 2, 2)
		runningLine += "  " + MutedStyle.Render(fmt.Sprintf("(%s · ↑ %s ↓ %s tokens)",
			formatDuration(elapsed), FormatTokens(m.RunStats.DisplayInput), FormatTokens(m.RunStats.DisplayOutput)))
		if m.Width > 0 {
			runningLine = truncate.StringWithTail(runningLine, uint(max(m.Width-2, 1)), "…")
		}
	}

	tasksTree := m.renderTaskTree()

	switch {
	case runningLine != "" && tasksTree != "":
		return runningLine + "\n" + tasksTree
	case runningLine != "":
		return runningLine
	case tasksTree != "":
		return tasksTree
	default:
		return ""
	}
}

// RenderPlanBar renders the plan review card. The full plan body is emitted
// into scrollback by the harness when exit_plan_mode succeeds (see
// internal/ui/plan.go: renderPlanForReview), so this card only asks for the
// user's decision and shows execution constraints.
//
//	┌─ "Ready to code?" (Accent border) ────────────┐
//	│ Plan ready: <title>                           │
//	│ Allowed command prefixes:                     │
//	│ - <cmd>                                       │
//	│                                               │
//	│   1. Execute plan                             │
//	│   2. Cancel                                   │
//	│   ───                                         │
//	│   3. Type here to request changes             │
//	│                                               │
//	│ Enter · ↑↓ · 1-3 · Esc cancel                 │
//	│ Ctrl+E to edit · ~/.codebot/plans/foo.md      │
//	└───────────────────────────────────────────────┘
//
// Empty when no review is in progress.
func (m *Model) RenderPlanBar() string {
	if m.config.StatusPlan == nil {
		return ""
	}
	plan := m.config.StatusPlan(m)
	if plan == nil || len(plan.Choices) == 0 {
		return ""
	}

	optionCount := len(plan.Choices) + 1 // +1 for "Type here"

	// Keep details and footer wrapped inside the card instead of letting long
	// plan file paths push the right border past the terminal width.
	ruleWidth := max(m.Width-6, 20)

	var renderedFooter string
	if !plan.OtherMode && plan.PlanFilePath != "" {
		footer := "Ctrl+E to edit in $EDITOR · " + plan.PlanFilePath
		renderedFooter = askHintStyle.Width(ruleWidth).Render(footer)
	}

	var b strings.Builder

	// Card title.
	b.WriteString(askQuestionStyle.Render("Ready to code?"))
	b.WriteString("\n\n")

	if title := strings.TrimSpace(plan.Title); title != "" {
		b.WriteString(askDescStyle.Render("Plan ready: " + title))
	} else {
		b.WriteString(askDescStyle.Render("Plan ready."))
	}
	b.WriteString("\n\n")

	// Allowed command prefixes (and any other plan review details).
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

	// "Type here" option — agent-neutral wording, no provider name.
	const otherLabel = "Type here to request changes"
	otherIdx := len(plan.Choices)
	otherNum := fmt.Sprintf("%d. ", otherIdx+1)
	if plan.Active == otherIdx {
		if plan.OtherMode {
			b.WriteString(askOptionActiveStyle.Render("> " + otherNum + plan.OtherBuf + "█"))
		} else {
			b.WriteString(askOptionActiveStyle.Render("> " + otherNum + otherLabel))
		}
	} else {
		b.WriteString(askOptionInactiveStyle.Render("  " + otherNum + otherLabel))
	}
	b.WriteString("\n\n")

	// Hint line — keys.
	if plan.OtherMode {
		b.WriteString(askHintStyle.Render("Enter to confirm · Esc to go back"))
	} else {
		b.WriteString(askHintStyle.Render(fmt.Sprintf("Enter to select · ↑↓ Navigate · 1-%d Shortcut · Esc to cancel", optionCount)))
	}

	// Footer — Ctrl+E + plan file path, pre-rendered above so long paths wrap
	// inside the card instead of pushing the right border off-screen.
	if renderedFooter != "" {
		b.WriteByte('\n')
		b.WriteString(renderedFooter)
	}

	return PlanBoxStyle.Render(b.String())
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
		chips = append(chips, ContextChipPathStyle.Render(filepath.Base(m.Cwd)))
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
// Kept at Subtle weight — peripheral metadata should recede, not announce.
func (m *Model) renderRunSummary() string {
	s := m.RunStats
	style := lipgloss.NewStyle().Foreground(Subtle)
	return strings.Join([]string{
		style.Render("※"),
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

// taskTreeMaxVisible caps the number of task rows shown in the live tree.
// Both the renderer and the recency-tick scheduler key off this cap, so it
// must be a single source of truth — if they ever disagree, scheduleRecencyTick
// might skip arming a re-render that the renderer is actually depending on.
const taskTreeMaxVisible = 5

// renderTaskTree renders the compact task tree pinned just below the
// Running line. Two layouts mirror Claude Code's TaskListV2:
//
//	nested (agent running, hangs off the Running spinner):
//	  ⎿  ☐ pending subject
//	     ▣ in-progress active form
//	     ✓ completed subject              (strikethrough)
//	     … +N pending, M completed
//
//	standalone (agent idle, no parent line above):
//	  7 tasks (3 done, 1 in progress, 3 open)
//	  ☐ pending subject
//	  …
//
// Pending and in-progress tasks always show. Completed tasks are capped
// (most recent few) with a roll-up line; this keeps the live area compact
// during long runs without losing momentum signal.
func (m *Model) renderTaskTree() string {
	snap := m.Tasks
	if snap == nil || snap.Total == 0 {
		return ""
	}

	// When the list fits, we keep creation order untouched. When truncation
	// kicks in, we mirror Claude Code's TaskListV2 priority groups so that
	// tasks completed within the last RecentCompletedTTL get pinned to the
	// top (visual celebration), while older completes sink to the bottom.
	items := snap.Items
	var visible, hiddenItems []storage.Task
	if len(items) <= taskTreeMaxVisible {
		visible = items
	} else {
		now := time.Now()
		var recentCompleted, olderCompleted, inProgress, pending, unknown []storage.Task
		for _, t := range items {
			switch t.Status {
			case storage.TaskInProgress:
				inProgress = append(inProgress, t)
			case storage.TaskPending:
				pending = append(pending, t)
			case storage.TaskCompleted:
				if t.CompletedAt != nil && now.Sub(*t.CompletedAt) < RecentCompletedTTL {
					recentCompleted = append(recentCompleted, t)
				} else {
					olderCompleted = append(olderCompleted, t)
				}
			default:
				// Unknown status (forward-compat): keep it visible so the
				// user isn't confused by a phantom overflow line hiding
				// items we can't classify. Sorted to the very bottom.
				unknown = append(unknown, t)
			}
		}
		prioritized := make([]storage.Task, 0, len(items))
		prioritized = append(prioritized, recentCompleted...)
		prioritized = append(prioritized, inProgress...)
		prioritized = append(prioritized, pending...)
		prioritized = append(prioritized, olderCompleted...)
		prioritized = append(prioritized, unknown...)
		cut := min(taskTreeMaxVisible, len(prioritized))
		visible = prioritized[:cut]
		hiddenItems = prioritized[cut:]
	}

	if m.Running {
		return renderTaskTreeNested(visible, hiddenItems)
	}
	return renderTaskTreeStandalone(snap, visible, hiddenItems)
}

// renderTaskTreeNested formats the tree as a child of the Running spinner
// line — connector pulls the eye down from the parent into the checklist.
// The connector hangs off the first task line so the tree visually attaches
// to the Running parent without a redundant "Tasks N/M" header row.
func renderTaskTreeNested(visible, hiddenItems []storage.Task) string {
	if len(visible) == 0 {
		return ""
	}

	var b strings.Builder

	const indent = "     " // 2 (margin) + 3 (TreeConnector width)
	for i, t := range visible {
		if i == 0 {
			b.WriteString("  ")
			b.WriteString(ConnectorStyle.Render(TreeConnector))
		} else {
			b.WriteByte('\n')
			b.WriteString(indent)
		}
		b.WriteString(renderTaskTreeLine(t))
	}
	if summary := taskOverflowSummary(hiddenItems); summary != "" {
		b.WriteByte('\n')
		b.WriteString(indent)
		b.WriteString(MutedStyle.Render(summary))
	}
	return b.String()
}

// renderTaskTreeStandalone formats the tree as a self-contained block
// (no parent line above), matching Claude Code's `isStandalone` layout:
// muted prose header with bold counts, marginLeft=2 for the whole box.
func renderTaskTreeStandalone(snap *storage.TaskSnapshot, visible, hiddenItems []storage.Task) string {
	var b strings.Builder

	const indent = "  " // marginLeft: 2
	b.WriteString(indent)
	b.WriteString(renderStandaloneHeader(snap))

	for _, t := range visible {
		b.WriteByte('\n')
		b.WriteString(indent)
		b.WriteString(renderTaskTreeLine(t))
	}
	if summary := taskOverflowSummary(hiddenItems); summary != "" {
		b.WriteByte('\n')
		b.WriteString(indent)
		b.WriteString(MutedStyle.Render(summary))
	}
	return b.String()
}

// renderStandaloneHeader builds the "N tasks (X done, Y in progress, Z open)"
// line. Whole line is rendered in the Muted color so it matches the overflow
// summary below it; numbers carry bold to keep counts scannable against the
// surrounding prose.
func renderStandaloneHeader(snap *storage.TaskSnapshot) string {
	num := MutedStyle.Bold(true)

	var b strings.Builder
	b.WriteString(num.Render(fmt.Sprintf("%d", snap.Total)))
	b.WriteString(MutedStyle.Render(" tasks ("))
	b.WriteString(num.Render(fmt.Sprintf("%d", snap.Completed)))
	b.WriteString(MutedStyle.Render(" done, "))
	if snap.InProgress > 0 {
		b.WriteString(num.Render(fmt.Sprintf("%d", snap.InProgress)))
		b.WriteString(MutedStyle.Render(" in progress, "))
	}
	b.WriteString(num.Render(fmt.Sprintf("%d", snap.Pending)))
	b.WriteString(MutedStyle.Render(" open)"))
	return b.String()
}

// taskOverflowSummary breaks down hidden tasks by status: empty if none,
// otherwise something like "… +1 in progress, 3 pending, 2 completed".
// Mirrors Claude Code's hiddenSummary order (in_progress → pending → completed).
func taskOverflowSummary(hidden []storage.Task) string {
	if len(hidden) == 0 {
		return ""
	}
	var pending, inProgress, completed int
	for _, t := range hidden {
		switch t.Status {
		case storage.TaskPending:
			pending++
		case storage.TaskInProgress:
			inProgress++
		case storage.TaskCompleted:
			completed++
		}
	}
	parts := make([]string, 0, 3)
	if inProgress > 0 {
		parts = append(parts, fmt.Sprintf("%d in progress", inProgress))
	}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", pending))
	}
	if completed > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", completed))
	}
	if len(parts) == 0 {
		// Only "unknown" items hidden — fall back to a generic count so we
		// still show the user that something was elided.
		return fmt.Sprintf("… +%d more", len(hidden))
	}
	return "… +" + strings.Join(parts, ", ")
}

// renderTaskTreeLine renders a single task as "<icon> <subject>" with
// status-appropriate styling. Completed tasks render strikethrough so the
// eye lands on what's still open.
func renderTaskTreeLine(t storage.Task) string {
	text := t.Subject
	if t.Status == storage.TaskInProgress && t.ActiveForm != "" {
		text = t.ActiveForm
	}

	var icon string
	var iconStyle lipgloss.Style
	var renderedText string

	switch t.Status {
	case storage.TaskPending:
		icon = "☐"
		iconStyle = MutedStyle
		// No foreground — let the terminal's default text color through so
		// the user's color scheme owns the look. Only the icon is muted.
		renderedText = text
	case storage.TaskInProgress:
		icon = "▣"
		iconStyle = lipgloss.NewStyle().Foreground(Accent)
		renderedText = lipgloss.NewStyle().Foreground(Text).Bold(true).Render(text)
	case storage.TaskCompleted:
		icon = "✓"
		iconStyle = lipgloss.NewStyle().Foreground(Success)
		// Mirrors Claude Code's <Text dimColor strikethrough>. We emit a single
		// SGR block (`ESC[2;9m … ESC[0m`) instead of going through lipgloss's
		// styled renderer because lipgloss wraps each rune in its own
		// open/close pair when strikethrough is enabled (per-char `ESC[0m`
		// resets), and many terminals fail to draw a continuous overstrike
		// line across those resets.
		renderedText = "\x1b[2;9m" + text + "\x1b[0m"
	default:
		icon = "○"
		iconStyle = MutedStyle
		renderedText = lipgloss.NewStyle().Foreground(Muted).Render(text)
	}

	return iconStyle.Render(icon) + " " + renderedText
}
