package tui

// Per-tool result renderers:
//   - Edit: diff formatting with intra-line highlighting for single-line changes.
//   - Filesystem: write preview, ls tree, read with line numbers.
//   - Subagent: header parsing, amber card, output+usage footer.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	reflowwrap "github.com/muesli/reflow/wrap"
)

// ---------------------------------------------------------------------------
// Edit
// ---------------------------------------------------------------------------

// RenderEditResult renders the edit tool result with colored diff output.
// Leads with a "⎿  Added N lines, removed M lines" summary. Single-line
// changes (1 removed + 1 added) get intra-line highlighting where only the
// changed portion is rendered with a deeper background tint.
//
// width is the terminal cells available to the diff body (caller already
// subtracts its own indent). The body is right-padded to that width so the
// background fill carries cleanly to the edge instead of stopping at the
// last code character.
func RenderEditResult(result json.RawMessage, width int) string {
	connector := ConnectorStyle.Render(TreeConnector)
	if len(result) == 0 {
		return connector + MutedStyle.Render("(edit completed)")
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		return connector + TruncateLines(string(result), 10)
	}

	msg, _ := parsed["message"].(string)
	diff, _ := parsed["diff"].(string)
	if diff == "" {
		if msg == "" {
			msg = "(edit completed)"
		}
		return connector + MutedStyle.Render(msg)
	}

	lines := strings.Split(diff, "\n")
	added, removed := countDiffLines(lines)
	stats := fmt.Sprintf("Added %d lines, removed %d lines", added, removed)

	var sb strings.Builder
	sb.WriteString(connector + MutedStyle.Render(stats) + "\n")

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
				remRendered, addRendered := renderIntraLineDiff(removed[0], added[0], width)
				sb.WriteString(remRendered + "\n")
				sb.WriteString(addRendered + "\n")
			} else {
				for _, r := range removed {
					sb.WriteString(renderDiffLine(r, DiffRemoveGutterStyle, DiffRemoveBodyStyle, width) + "\n")
				}
				for _, a := range added {
					sb.WriteString(renderDiffLine(a, DiffAddGutterStyle, DiffAddBodyStyle, width) + "\n")
				}
			}
			continue
		}

		if strings.HasPrefix(line, "+") {
			sb.WriteString(renderDiffLine(line, DiffAddGutterStyle, DiffAddBodyStyle, width) + "\n")
		} else {
			sb.WriteString(MutedStyle.Render(line) + "\n")
		}
		i++
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderDiffLine renders one diff line as gutter (line-number + sigil, fg+bg)
// followed by body (code, bg only). The body is padded with spaces to width
// so the background fill carries to the right edge — without padding, lipgloss
// stops the bg at the last code character and the visual band looks broken.
func renderDiffLine(line string, gutter, body lipgloss.Style, width int) string {
	prefix, content := splitDiffPrefix(line)
	used := lipgloss.Width(prefix) + lipgloss.Width(content)
	if pad := width - used; pad > 0 {
		content += strings.Repeat(" ", pad)
	}
	return gutter.Render(prefix) + body.Render(content)
}

// renderIntraLineDiff highlights the changed substring within a single-line
// change. The unchanged head/tail use the base bg tint; the changed middle
// uses the deeper bg tint. Foreground is never overridden so syntax
// highlighting (when present) survives.
//
// width controls the right-edge padding so the bg fill reaches the terminal
// edge instead of stopping at the last code character.
// Uses rune-level comparison to avoid splitting multi-byte UTF-8 characters.
func renderIntraLineDiff(removedLine, addedLine string, width int) (string, string) {
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

	rem := composeIntraLine(remPrefix, commonPre, remMid, commonSuf, width,
		DiffRemoveGutterStyle, DiffRemoveBodyStyle, DiffRemoveInverseStyle)
	add := composeIntraLine(addPrefix, commonPre, addMid, commonSuf, width,
		DiffAddGutterStyle, DiffAddBodyStyle, DiffAddInverseStyle)
	return rem, add
}

// composeIntraLine assembles one side of an intra-line diff: gutter (fg+bg),
// then body split into common-pre / changed-mid / common-suf with the changed
// segment in the deeper bg shade. Trailing pad keeps the bg fill flush right.
func composeIntraLine(prefix, pre, mid, suf string, width int, gutter, body, emphasis lipgloss.Style) string {
	used := lipgloss.Width(prefix) + lipgloss.Width(pre) + lipgloss.Width(mid) + lipgloss.Width(suf)
	pad := ""
	if remaining := width - used; remaining > 0 {
		pad = strings.Repeat(" ", remaining)
	}

	var sb strings.Builder
	sb.WriteString(gutter.Render(prefix))
	if pre != "" {
		sb.WriteString(body.Render(pre))
	}
	if mid != "" {
		sb.WriteString(emphasis.Render(mid))
	}
	if suf != "" || pad != "" {
		sb.WriteString(body.Render(suf + pad))
	}
	return sb.String()
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

// ---------------------------------------------------------------------------
// Filesystem (write / ls / read)
// ---------------------------------------------------------------------------

// RenderWriteResult renders the write completion as a summary. The content
// preview is already emitted during the preview update, so repeating it here
// duplicates the same file body in scrollback.
func RenderWriteResult(result json.RawMessage) string {
	prefix := ToolIconStyle.Render("✓  ")
	if len(result) == 0 {
		return prefix + MutedStyle.Render("(file written)")
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		return prefix + TruncateLines(string(result), 10)
	}

	msg, _ := parsed["message"].(string)
	if msg == "" {
		msg = "(file written)"
	}
	return prefix + MutedStyle.Render(msg)
}

// countDiffLines counts +/- prefixed lines in a diff, ignoring +++ and ---
// header markers if present.
func countDiffLines(lines []string) (added, removed int) {
	for _, ln := range lines {
		if strings.HasPrefix(ln, "+++") || strings.HasPrefix(ln, "---") {
			continue
		}
		if strings.HasPrefix(ln, "+") {
			added++
		} else if strings.HasPrefix(ln, "-") {
			removed++
		}
	}
	return added, removed
}

// RenderLsResult renders ls tool results with tree structure.
// Returns the directory path (for header update) and the formatted body.
func RenderLsResult(result json.RawMessage) (dirPath string, body string) {
	text := FormatToolResult(result, false)
	if text == "" {
		return "", "(no output)"
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 0 {
		return "", "(no output)"
	}

	// First line is the directory path.
	dirPath = strings.TrimSpace(lines[0])
	remaining := lines[1:]
	if len(remaining) == 0 {
		return dirPath, ""
	}

	maxVisible := ToolResultMaxLines
	hidden := 0
	if len(remaining) > maxVisible {
		hidden = len(remaining) - maxVisible
		remaining = remaining[:maxVisible]
	}

	connector := ConnectorStyle.Render(TreeConnector)
	contentStyle := lipgloss.NewStyle()

	var sb strings.Builder
	for i, line := range remaining {
		if i == 0 {
			sb.WriteString(connector)
		} else {
			sb.WriteString(ConnectorPad)
		}
		sb.WriteString(renderToolOutputLine(line, contentStyle))
		sb.WriteByte('\n')
	}
	if hidden > 0 {
		sb.WriteString(MutedStyle.Render(fmt.Sprintf("%s… +%d lines", ConnectorPad, hidden)))
		sb.WriteByte('\n')
	}
	return dirPath, strings.TrimRight(sb.String(), "\n")
}

// RenderReadSummary renders a one-line summary for the read tool ("Read N lines"),
// avoiding dumping file contents into the log.
func RenderReadSummary(result json.RawMessage) string {
	connector := ConnectorStyle.Render(TreeConnector)
	text := FormatToolResult(result, false)
	if text == "" {
		return connector + MutedStyle.Render("(empty)")
	}
	n := strings.Count(text, "\n") + 1
	noun := "lines"
	if n == 1 {
		noun = "line"
	}
	return connector + MutedStyle.Render(fmt.Sprintf("Read %d %s", n, noun))
}

// RenderReadResult renders glob tool results as a path list with colored line numbers.
// Handles both numbered lines ("  123\tcontent") and plain path lists.
func RenderReadResult(result json.RawMessage) string {
	text := FormatToolResult(result, false)
	if text == "" {
		return "(no output)"
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	maxVisible := ToolResultMaxLines
	hidden := 0
	if len(lines) > maxVisible {
		hidden = len(lines) - maxVisible
		lines = lines[:maxVisible]
	}

	connector := ConnectorStyle.Render(TreeConnector)
	lineNumStyle := lipgloss.NewStyle().Foreground(Meta)
	contentStyle := lipgloss.NewStyle()

	var sb strings.Builder
	for i, line := range lines {
		if i == 0 {
			sb.WriteString(connector)
		} else {
			sb.WriteString(ConnectorPad)
		}
		// Split "  123\tcontent" into line number and content.
		if idx := strings.IndexByte(line, '\t'); idx >= 0 {
			sb.WriteString(lineNumStyle.Render(line[:idx+1]))
			sb.WriteString(contentStyle.Render(line[idx+1:]))
		} else {
			sb.WriteString(renderToolOutputLine(line, contentStyle))
		}
		sb.WriteByte('\n')
	}
	if hidden > 0 {
		sb.WriteString(MutedStyle.Render(fmt.Sprintf("%s… +%d lines", ConnectorPad, hidden)))
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ---------------------------------------------------------------------------
// Subagent
// ---------------------------------------------------------------------------

// parseSubagentHeader extracts agent name and task hint from subagent tool args.
func parseSubagentHeader(args json.RawMessage) (name, hint string) {
	if len(args) == 0 {
		return "subagent", ""
	}
	var p struct {
		Agent string          `json:"agent"`
		Task  string          `json:"task"`
		Tasks json.RawMessage `json:"tasks"`
		Chain json.RawMessage `json:"chain"`
	}
	if json.Unmarshal(args, &p) != nil {
		return "subagent", ""
	}
	if p.Agent != "" {
		return p.Agent, p.Task
	}
	if len(p.Tasks) > 2 { // not empty "[]"
		return "parallel", ""
	}
	if len(p.Chain) > 2 {
		return "chain", ""
	}
	return "subagent", ""
}

// renderSubagentCard wraps content in an amber-bordered card for visual distinction.
// Content exceeding roughly one screen height is truncated with a line-count hint.
func (m *Model) renderSubagentCard(content string) string {
	w := max(m.Width, 30)
	cw := w - 6 // 2(indent) + 2(border) + 2(padding)
	wrapped := strings.TrimRight(reflowwrap.String(content, cw), "\n")

	// Truncate to approximately one screen height.
	maxLines := max(m.Height-4, 12)
	lines := strings.Split(wrapped, "\n")
	if len(lines) > maxLines {
		remaining := len(lines) - maxLines
		wrapped = strings.Join(lines[:maxLines], "\n") +
			"\n" + MutedStyle.Render(fmt.Sprintf("... (%d more lines)", remaining))
	}

	// Muted tone — subagent output is secondary to the main conversation.
	wrapped = MutedStyle.Render(wrapped)

	return SubagentCardStyle.Width(w - 4).Render(wrapped)
}

// FormatSubagentOutput extracts the full output from a subagent result,
// appending usage stats as a footer. Returns content for card display.
func FormatSubagentOutput(result json.RawMessage) string {
	if len(result) == 0 {
		return "(no output)"
	}

	var obj struct {
		Output string `json:"output"`
		Error  string `json:"error"`
		Usage  *struct {
			Input  int     `json:"input"`
			Output int     `json:"output"`
			Turns  int     `json:"turns"`
			Tools  int     `json:"tools"`
			Cost   float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(result, &obj); err != nil {
		return FormatToolResult(result, false)
	}

	if obj.Error != "" {
		return "error: " + obj.Error
	}

	output := strings.TrimSpace(obj.Output)
	if output == "" {
		// No "output" field — parallel result, background ack, etc.
		return FormatToolResult(result, false)
	}

	// Append usage stats footer.
	if u := obj.Usage; u != nil {
		stats := fmt.Sprintf("%d turns · %d tools · ↑%s ↓%s tokens",
			u.Turns, u.Tools, FormatTokens(u.Input), FormatTokens(u.Output))
		output += "\n\n" + MutedStyle.Render(stats)
	}
	return output
}
