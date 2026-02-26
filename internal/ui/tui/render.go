package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
	reflowwrap "github.com/muesli/reflow/wrap"
)

// ---------------------------------------------------------------------------
// Live area renderers (used by View)
// ---------------------------------------------------------------------------

func (m *Model) renderWelcome() string {
	title := WelcomeTitleStyle.Render("◆ Codebot")

	info := m.ModelName
	if m.Cwd != "" {
		info += " · " + shortenPath(m.Cwd)
		if m.GitBranch != "" {
			info += " (" + m.GitBranch + ")"
		}
	}
	hints := "Enter send · Ctrl+J newline · Esc abort · /help commands"

	content := title + "\n" +
		WelcomeDetailStyle.Render(info) + "\n\n" +
		MutedStyle.Render(hints)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1).
		Render(content)

	return box
}

// RenderStatusBar renders the status line above the input (running state + turn).
func (m *Model) RenderStatusBar() string {
	var plan *PlanBarInfo
	if m.config.StatusPlan != nil {
		plan = m.config.StatusPlan(m)
	}

	var status string
	if m.Running {
		status = m.Spinner.View() + " running"
	} else {
		status = lipgloss.NewStyle().Foreground(ColorSuccess).Render("●") + " ready"
	}
	status += "  " + MutedStyle.Render(fmt.Sprintf("turn %d", m.TurnCount))

	// Append plan mode tag.
	if plan != nil && plan.Tag != "" {
		status += "  " + PlanTagStyle.Render("◇ "+plan.Tag)
	}

	if m.Width > 0 {
		status = truncate.StringWithTail(status, uint(max(m.Width-2, 1)), "…")
	}
	return status
}

// RenderPlanBar renders the plan choices bar (empty string when inactive).
func (m *Model) RenderPlanBar() string {
	if m.config.StatusPlan == nil {
		return ""
	}
	plan := m.config.StatusPlan(m)
	if plan == nil || len(plan.Choices) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, c := range plan.Choices {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if i == plan.Active {
			sb.WriteString(ChoiceActiveStyle.Render("▸ " + c))
		} else {
			sb.WriteString(ChoiceInactiveStyle.Render("  " + c))
		}
	}
	return FooterStyle.Width(m.Width).Render(sb.String())
}

// RenderContextBar renders the context line below the input (env info).
func (m *Model) RenderContextBar() string {
	var parts []string
	if m.GitBranch != "" {
		parts = append(parts, m.GitBranch)
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

// ---------------------------------------------------------------------------
// Markdown rendering
// ---------------------------------------------------------------------------

// RenderMarkdown renders markdown content using glamour.
func (m *Model) RenderMarkdown(content string) string {
	if m.Glamour == nil || content == "" {
		return content
	}
	rendered, err := m.Glamour.Render(content)
	if err != nil {
		return content
	}
	// glamour "notty" adds a uniform left margin to every line.
	// dedent must run FIRST to detect and strip the common indent;
	// if TrimSpace runs first it strips only line 1's margin, making
	// minIndent=0 and dedent a no-op — leaving lines 2+ over-indented.
	return strings.TrimSpace(dedent(rendered))
}

// ---------------------------------------------------------------------------
// Run summary
// ---------------------------------------------------------------------------

// renderRunSummary renders per-run stats shown after agent completion.
func (m *Model) renderRunSummary() string {
	s := m.RunStats
	return MutedStyle.Render(fmt.Sprintf("─ %d turns · %d tools · ↑%s ↓%s tokens",
		s.Turns, s.ToolCalls, FormatTokens(s.Input), FormatTokens(s.Output)))
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

// ---------------------------------------------------------------------------
// Tool display utilities
// ---------------------------------------------------------------------------

// FormatToolArgs formats tool arguments for display, truncating if needed.
func FormatToolArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(args))
	if s == "{}" || s == "null" {
		return ""
	}
	if len([]rune(s)) > 100 {
		s = string([]rune(s)[:97]) + "..."
	}
	return s
}

// FormatToolResult formats a tool result for display.
func FormatToolResult(result json.RawMessage, isError bool) string {
	prefix := ""
	if isError {
		prefix = "error: "
	}
	if len(result) == 0 {
		return prefix + "(no output)"
	}

	// Extract "message" from JSON objects for cleaner display.
	var obj map[string]any
	if json.Unmarshal(result, &obj) == nil {
		if msg, ok := obj["message"].(string); ok && msg != "" {
			return prefix + msg
		}
	}

	s := strings.TrimSpace(string(result))
	lines := strings.SplitN(s, "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
		s = strings.Join(lines, "\n") + "\n..."
	}
	if len([]rune(s)) > 300 {
		s = string([]rune(s)[:297]) + "..."
	}
	return prefix + s
}

// TruncateLines truncates text to maxLines, appending "..." if truncated.
func TruncateLines(s string, maxLines int) string {
	lines := strings.SplitN(s, "\n", maxLines+1)
	if len(lines) > maxLines {
		return strings.Join(lines[:maxLines], "\n") + "..."
	}
	return s
}

// RenderStreamingOutput shows the last N lines of streaming tool output.
func RenderStreamingOutput(full string, maxLines int) string {
	all := strings.TrimRight(full, "\n")
	lines := strings.Split(all, "\n")
	start := max(len(lines)-maxLines, 0)
	visible := lines[start:]
	var sb strings.Builder
	if start > 0 {
		sb.WriteString(MutedStyle.Render(fmt.Sprintf("... (%d lines above)", start)))
		sb.WriteByte('\n')
	}
	for _, line := range visible {
		sb.WriteString(MutedStyle.Render(line))
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// RenderEditResult renders the edit tool result with colored diff output.
// Single-line changes (1 removed + 1 added) get intra-line highlighting
// where only the changed portion is rendered in inverse color.
func RenderEditResult(result json.RawMessage) string {
	if len(result) == 0 {
		return "(edit completed)"
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		return TruncateLines(string(result), 10)
	}

	msg, _ := parsed["message"].(string)
	diff, _ := parsed["diff"].(string)
	if diff == "" {
		return msg
	}

	lines := strings.Split(diff, "\n")

	var sb strings.Builder
	sb.WriteString(msg + "\n")

	i := 0
	for i < len(lines) {
		line := expandTabs(lines[i])
		if line == "" {
			i++
			continue
		}

		// Collect consecutive '-' lines
		if strings.HasPrefix(line, "-") {
			var removed []string
			for i < len(lines) && strings.HasPrefix(expandTabs(lines[i]), "-") {
				removed = append(removed, expandTabs(lines[i]))
				i++
			}
			// Collect immediately following '+' lines
			var added []string
			for i < len(lines) && strings.HasPrefix(expandTabs(lines[i]), "+") {
				added = append(added, expandTabs(lines[i]))
				i++
			}
			// Single-line change: intra-line diff
			if len(removed) == 1 && len(added) == 1 {
				remRendered, addRendered := renderIntraLineDiff(removed[0], added[0])
				sb.WriteString(remRendered + "\n")
				sb.WriteString(addRendered + "\n")
			} else {
				for _, r := range removed {
					sb.WriteString(DiffRemoveStyle.Render(r) + "\n")
				}
				for _, a := range added {
					sb.WriteString(DiffAddStyle.Render(a) + "\n")
				}
			}
			continue
		}

		if strings.HasPrefix(line, "+") {
			sb.WriteString(DiffAddStyle.Render(line) + "\n")
		} else {
			sb.WriteString(MutedStyle.Render(line) + "\n")
		}
		i++
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderIntraLineDiff highlights the specific changed portion within a single-line change.
// It finds the common prefix/suffix of the content (after the line-number prefix),
// then renders unchanged parts in base color and changed parts in inverse color.
// Uses rune-level comparison to avoid splitting multi-byte UTF-8 characters.
func renderIntraLineDiff(removedLine, addedLine string) (string, string) {
	// Split off the diff prefix (e.g. "-  5 " or "+  5 ")
	remPrefix, remContent := splitDiffPrefix(removedLine)
	addPrefix, addContent := splitDiffPrefix(addedLine)

	remRunes := []rune(remContent)
	addRunes := []rune(addContent)

	// Find common prefix length (in runes)
	prefixLen := 0
	minLen := min(len(remRunes), len(addRunes))
	for prefixLen < minLen && remRunes[prefixLen] == addRunes[prefixLen] {
		prefixLen++
	}

	// Find common suffix length (in runes, not overlapping prefix)
	suffixLen := 0
	for suffixLen < minLen-prefixLen &&
		remRunes[len(remRunes)-1-suffixLen] == addRunes[len(addRunes)-1-suffixLen] {
		suffixLen++
	}

	// Convert back to strings
	commonPre := string(remRunes[:prefixLen])
	remMid := string(remRunes[prefixLen : len(remRunes)-suffixLen])
	addMid := string(addRunes[prefixLen : len(addRunes)-suffixLen])
	commonSuf := string(remRunes[len(remRunes)-suffixLen:])

	// Build rendered lines
	var remSB, addSB strings.Builder
	remSB.WriteString(DiffRemoveStyle.Render(remPrefix + commonPre))
	if remMid != "" {
		remSB.WriteString(DiffInverseRemoveStyle.Render(remMid))
	}
	remSB.WriteString(DiffRemoveStyle.Render(commonSuf))

	addSB.WriteString(DiffAddStyle.Render(addPrefix + commonPre))
	if addMid != "" {
		addSB.WriteString(DiffInverseAddStyle.Render(addMid))
	}
	addSB.WriteString(DiffAddStyle.Render(commonSuf))

	return remSB.String(), addSB.String()
}

// splitDiffPrefix splits a diff line like "-  5 content" into prefix ("-  5 ") and content ("content").
// Handles space-padded line numbers from fmt's %*d format: sign + spaces + digits + space.
func splitDiffPrefix(line string) (prefix, content string) {
	// Skip the leading +/- sign
	i := 1
	// Skip padding spaces (from %*d right-justification)
	for i < len(line) && line[i] == ' ' {
		i++
	}
	// Skip digits (line number)
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	// Skip the space separator
	if i < len(line) && line[i] == ' ' {
		i++
	}
	return line[:i], line[i:]
}

// expandTabs replaces tab characters with 4 spaces for consistent display.
func expandTabs(s string) string {
	return strings.ReplaceAll(s, "\t", "    ")
}

// FormatProgressLine formats a tool progress update for display.
func FormatProgressLine(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	s := string(result)
	if len([]rune(s)) > 200 {
		s = string([]rune(s)[:197]) + "..."
	}
	return s
}
