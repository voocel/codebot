package tui

// UI chrome around the input area: status/context bars above and below,
// slash-command palette, per-run summary, queued messages, task progress card.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/reflow/truncate"

	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/todo"
)

// ---------------------------------------------------------------------------
// Status / context bars
// ---------------------------------------------------------------------------

// RenderStatusBar renders the live status block pinned above the input:
// the Running spinner line (when the agent is active) plus a compact task
// tree (when there are tasks). Either component may be empty depending on
// state — the task tree stays visible between turns so users can track
// progress at idle without losing the momentum view.
func (m *Model) RenderStatusBar() string {
	if m.Dialogs.active() != nil {
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

	tasksTree := m.renderTodoTree()

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
	if mode := modeChip(m.Mode); mode != "" {
		chips = append(chips, ContextChipAccentStyle.Render(mode))
	}
	if m.Cwd != "" {
		chips = append(chips, ContextChipPathStyle.Render(filepath.Base(m.Cwd)))
	}
	chips = append(chips, ContextChipStyle.Render(m.formatModelChip()))
	if usage := m.usageChip(); usage != "" {
		chips = append(chips, ContextChipStyle.Render(usage))
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

// modeChip names the permission modes that change what runs unasked.
func modeChip(mode interact.Mode) string {
	switch mode {
	case interact.ModeStrict:
		return "◆ strict"
	case interact.ModeAcceptEdits:
		return "⏵⏵ accept edits"
	case interact.ModeTrust:
		return "⏵⏵ trust"
	default:
		return ""
	}
}

// usageChip shows how full the context is, the tokens spent and the cost.
func (m *Model) usageChip() string {
	st := m.Status
	var parts []string
	if st.Window > 0 && st.Context > 0 {
		parts = append(parts, fmt.Sprintf("ctx: %.0f%%", float64(st.Context)*100/float64(st.Window)))
	}
	if st.Usage.Input+st.Usage.Output > 0 {
		parts = append(parts, fmt.Sprintf("↑%s ↓%s", FormatTokens(st.Usage.Input), FormatTokens(st.Usage.Output)))
	}
	if st.Usage.Cost != nil && st.Usage.Cost.Total > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", st.Usage.Cost.Total))
	}
	if len(parts) == 0 {
		return ""
	}
	return TokenStyle.Render(strings.Join(parts, " · "))
}

func (m *Model) formatModelChip() string {
	s := m.Status.Model
	if m.Status.Window > 0 {
		s += " (" + FormatTokens(m.Status.Window) + ")"
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
	desc := ansi.Truncate(item.Description, descMaxWidth, "…")
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
	default:
		return ""
	}
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
	return MutedStyle.Render(ansi.Truncate(strings.Join(parts, " · "), width, "…"))
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
		text := ansi.Truncate(msg, 80, "…")
		b.WriteString(QueuedMsgStyle.Render("  ↳ " + text))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// todoTreeMaxVisible caps the number of todo rows shown in the live tree.
const todoTreeMaxVisible = 5

// renderTodoTree renders the compact todo tree pinned just below the Running
// line. Two layouts:
//
//	nested (agent running, hangs off the Running spinner):
//	  ⎿  ☐ pending item
//	     ▣ in-progress item
//	     ✓ completed item                 (strikethrough)
//	     … +N pending, M completed
//
//	standalone (agent idle, no parent line above):
//	  7 todos (3 done, 1 in progress, 3 open)
//	  ☐ pending item
//	  …
//
// A list that fits keeps the model's order. A longer one shows the item in
// progress and the open ones first, completed last, with a roll-up line for
// what does not fit.
func (m *Model) renderTodoTree() string {
	items := m.Todos
	if len(items) == 0 {
		return ""
	}

	visible, hidden := items, []todo.Item(nil)
	if len(items) > todoTreeMaxVisible {
		prioritized := make([]todo.Item, 0, len(items))
		for _, status := range []todo.Status{todo.InProgress, todo.Pending, todo.Completed} {
			for _, it := range items {
				if it.Status == status {
					prioritized = append(prioritized, it)
				}
			}
		}
		visible, hidden = prioritized[:todoTreeMaxVisible], prioritized[todoTreeMaxVisible:]
	}

	if m.Running {
		return renderTodoTreeNested(visible, hidden)
	}
	return renderTodoTreeStandalone(items, visible, hidden)
}

// renderTodoTreeNested formats the tree as a child of the Running spinner
// line. The connector hangs off the first line so the tree visually attaches
// to the Running parent without a redundant header row.
func renderTodoTreeNested(visible, hidden []todo.Item) string {
	var b strings.Builder
	const indent = "     " // 2 (margin) + 3 (TreeConnector width)
	for i, it := range visible {
		if i == 0 {
			b.WriteString("  ")
			b.WriteString(ConnectorStyle.Render(TreeConnector))
		} else {
			b.WriteByte('\n')
			b.WriteString(indent)
		}
		b.WriteString(renderTodoLine(it))
	}
	if summary := todoOverflowSummary(hidden); summary != "" {
		b.WriteByte('\n')
		b.WriteString(indent)
		b.WriteString(MutedStyle.Render(summary))
	}
	return b.String()
}

// renderTodoTreeStandalone formats the tree as a self-contained block (no
// parent line above): muted prose header with bold counts, marginLeft=2.
func renderTodoTreeStandalone(items, visible, hidden []todo.Item) string {
	var b strings.Builder
	const indent = "  "
	b.WriteString(indent)
	b.WriteString(renderTodoHeader(items))
	for _, it := range visible {
		b.WriteByte('\n')
		b.WriteString(indent)
		b.WriteString(renderTodoLine(it))
	}
	if summary := todoOverflowSummary(hidden); summary != "" {
		b.WriteByte('\n')
		b.WriteString(indent)
		b.WriteString(MutedStyle.Render(summary))
	}
	return b.String()
}

// renderTodoHeader builds the "N todos (X done, Y in progress, Z open)" line.
func renderTodoHeader(items []todo.Item) string {
	pending, inProgress, completed := todo.Counts(items)
	num := MutedStyle.Bold(true)

	var b strings.Builder
	b.WriteString(num.Render(fmt.Sprintf("%d", len(items))))
	b.WriteString(MutedStyle.Render(" todos ("))
	b.WriteString(num.Render(fmt.Sprintf("%d", completed)))
	b.WriteString(MutedStyle.Render(" done, "))
	if inProgress > 0 {
		b.WriteString(num.Render(fmt.Sprintf("%d", inProgress)))
		b.WriteString(MutedStyle.Render(" in progress, "))
	}
	b.WriteString(num.Render(fmt.Sprintf("%d", pending)))
	b.WriteString(MutedStyle.Render(" open)"))
	return b.String()
}

// todoOverflowSummary breaks down hidden items by status: empty if none,
// otherwise something like "… +1 in progress, 3 pending, 2 completed".
func todoOverflowSummary(hidden []todo.Item) string {
	if len(hidden) == 0 {
		return ""
	}
	pending, inProgress, completed := todo.Counts(hidden)
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
	return "… +" + strings.Join(parts, ", ")
}

// renderTodoLine renders one item as "<icon> <content>" with status-specific
// styling. Completed items render strikethrough so the eye lands on what's
// still open.
func renderTodoLine(it todo.Item) string {
	switch it.Status {
	case todo.InProgress:
		return lipgloss.NewStyle().Foreground(Accent).Render("▣") + " " +
			lipgloss.NewStyle().Foreground(Text).Bold(true).Render(it.Content)
	case todo.Completed:
		// Emit a single SGR block (`ESC[2;9m … ESC[0m`) for dim+strikethrough
		// instead of going through lipgloss's styled renderer: lipgloss wraps
		// each rune in its own open/close pair when strikethrough is enabled
		// (per-char `ESC[0m` resets), and many terminals fail to draw a
		// continuous overstrike line across those resets.
		return lipgloss.NewStyle().Foreground(Success).Render("✓") + " \x1b[2;9m" + it.Content + "\x1b[0m"
	default:
		// No foreground on the text — the terminal's default color owns the
		// look. Only the icon is muted.
		return MutedStyle.Render("☐") + " " + it.Content
	}
}
