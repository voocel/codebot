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
	if m.config.StatusTeam != nil {
		if t := m.config.StatusTeam(m); t != "" {
			chips = append(chips, ContextChipTeamStyle.Render(t))
		}
	}
	if m.config.StatusGoal != nil {
		if goal := m.config.StatusGoal(m); goal != "" {
			chips = append(chips, ContextChipAccentStyle.Render(goal))
		}
	}
	if m.Cwd != "" {
		chips = append(chips, ContextChipPathStyle.Render(filepath.Base(m.Cwd)))
	}
	chips = append(chips, ContextChipStyle.Render(m.formatModelChip()))
	if m.config.StatusRight != nil {
		if extra := m.config.StatusRight(m); extra != "" {
			chips = append(chips, ContextChipStyle.Render(extra))
		}
	}
	// Join chips with a dim vertical bar so adjacent ones don't visually
	// merge — previously each chip prefixed itself with "· " which read as
	// part of the chip content (e.g. "Ctrl+O to view" ran into "agent" of
	// the model chip behind it).
	separator := contextChipSeparatorStyle.Render(" │ ")
	line := strings.Join(chips, separator)
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

	// Reserve 2 cols on the right as safety against terminal-edge wrapping.
	// Flush-left to align with the status/context bar above.
	width := 74
	if m.Width > 0 {
		width = min(max(m.Width-2, 34), 92)
	}

	list, remaining := m.renderCommandPaletteList(width)
	lines := []string{list}
	if footer := renderCommandPaletteFooter(selected, remaining, width); footer != "" {
		lines = append(lines, footer)
	}
	lines = append(lines, CommandPaletteHintStyle.Render("↑↓ move · Tab complete · Enter run/fill · Esc close"))

	out := strings.Join(lines, "\n")

	// Pad below to keep total height stable (prevents input jumping when the
	// candidate count changes between renders).
	visibleRows := min(len(m.compItems), PaletteMaxVisible)
	padLines := PaletteMaxVisible - visibleRows
	if padLines > 0 {
		out += strings.Repeat("\n", padLines)
	}
	return out
}

func (m *Model) renderCommandPaletteList(width int) (string, int) {
	start, end := commandPaletteWindow(len(m.compItems), m.compIdx, PaletteMaxVisible)
	lines := make([]string, 0, PaletteMaxVisible)

	for i := start; i < end; i++ {
		lines = append(lines, renderCommandPaletteRow(m.compItems[i], width, i == m.compIdx))
	}

	return strings.Join(lines, "\n"), len(m.compItems) - end
}

// paletteTagSlotWidth reserves a fixed-width column at the right of every
// row so that tagged and untagged rows align to the same desc column. The
// longest tag is "[custom]" (8) and we want 2 cols of breathing room.
const paletteTagSlotWidth = 10

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
	// 4 = marker(1) + space(1) + gap(1) + safety(1)
	descMaxWidth := max(width-nameWidth-4-paletteTagSlotWidth, 10)
	desc := truncateByWidth(item.Description, descMaxWidth)
	// Pad desc so the trailing tag column lines up across rows.
	descText := desc + strings.Repeat(" ", max(descMaxWidth-lipgloss.Width(desc), 0))

	var trailing string
	if tag := paletteKindTag(item.Kind); tag != "" {
		trailing = "  " + CommandPaletteTagStyle.Render(tag)
	}

	prefix := marker + " "
	if selected {
		return CommandPaletteSelectedStyle.Render(prefix) + CommandPaletteSelectedStyle.Render(nameText) + " " + CommandPaletteSelectedDescStyle.Render(descText) + trailing
	}
	return prefix + CommandPaletteItemStyle.Render(nameText) + " " + CommandPaletteDescStyle.Render(descText) + trailing
}

// paletteKindTag returns the trailing label for a command Kind, or "" for
// the builtin baseline (no tag → most rows stay visually quiet).
func paletteKindTag(kind string) string {
	switch kind {
	case "skill":
		return "[skill]"
	case "custom":
		return "[custom]"
	default:
		return ""
	}
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

// renderCommandPaletteFooter is shown only when there is real metadata to
// surface: hidden-overflow count and/or aliases for the selected item.
// Returns "" when neither applies — the caller skips the line entirely.
func renderCommandPaletteFooter(item CompletionItem, remaining, width int) string {
	var parts []string
	if remaining > 0 {
		parts = append(parts, fmt.Sprintf("… +%d more", remaining))
	}
	if len(item.Aliases) > 0 {
		aliases := make([]string, 0, len(item.Aliases))
		for _, alias := range item.Aliases {
			aliases = append(aliases, "/"+alias)
		}
		parts = append(parts, strings.Join(aliases, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return MutedStyle.Render(truncateByWidth(strings.Join(parts, " · "), width))
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
// Running line. Two layouts:
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

	// When the list fits, keep creation order untouched. When truncation kicks
	// in, priority groups apply: tasks completed within the last
	// RecentCompletedTTL get pinned to the top (visual celebration), older
	// completes sink to the bottom.
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

// renderTaskTreeStandalone formats the tree as a self-contained block (no
// parent line above): muted prose header with bold counts, marginLeft=2 for
// the whole box.
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
// Order: in_progress → pending → completed.
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
		// Emit a single SGR block (`ESC[2;9m … ESC[0m`) for dim+strikethrough
		// instead of going through lipgloss's styled renderer: lipgloss wraps
		// each rune in its own open/close pair when strikethrough is enabled
		// (per-char `ESC[0m` resets), and many terminals fail to draw a
		// continuous overstrike line across those resets.
		renderedText = "\x1b[2;9m" + text + "\x1b[0m"
	default:
		icon = "○"
		iconStyle = MutedStyle
		renderedText = lipgloss.NewStyle().Foreground(Muted).Render(text)
	}

	return iconStyle.Render(icon) + " " + renderedText
}
