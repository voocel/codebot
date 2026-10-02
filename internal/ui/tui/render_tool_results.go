package tui

// Per-tool result renderers:
//   - Edit: diff formatting with intra-line highlighting for single-line changes.
//   - Filesystem: write preview, ls tree, read with line numbers.
//   - Subagent: header parsing, amber card.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	reflowwrap "github.com/muesli/reflow/wrap"

	"github.com/voocel/codebot/internal/ui/tui/syntax"
)

// ---------------------------------------------------------------------------
// Edit
// ---------------------------------------------------------------------------

// RenderDiff renders a diff of the edit and write tools in color, under a
// line counting its changes. Single-line changes get intra-line highlighting
// (only the changed portion uses a deeper bg). filePath selects the chroma
// lexer; width is the body cells available for right-padding so the bg band
// reaches the edge instead of stopping at the last code character.
func RenderDiff(diff string, filePath string, width int) string {
	connector := ConnectorStyle.Render(TreeConnector)
	if diff == "" {
		return connector + MutedStyle.Render("(no changes)")
	}

	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
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
				remRendered, addRendered := renderIntraLineDiff(removed[0], added[0], filePath, width)
				sb.WriteString(remRendered + "\n")
				sb.WriteString(addRendered + "\n")
			} else {
				for _, r := range removed {
					sb.WriteString(renderDiffLine(r, DiffRemoveGutterStyle, DiffRemoveBodyStyle, filePath, width) + "\n")
				}
				for _, a := range added {
					sb.WriteString(renderDiffLine(a, DiffAddGutterStyle, DiffAddBodyStyle, filePath, width) + "\n")
				}
			}
			continue
		}

		if strings.HasPrefix(line, "+") {
			sb.WriteString(renderDiffLine(line, DiffAddGutterStyle, DiffAddBodyStyle, filePath, width) + "\n")
		} else {
			sb.WriteString(renderContextLine(line, filePath) + "\n")
		}
		i++
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderContextLine renders an unchanged diff line: muted gutter + highlighted
// body, no bg tint. Highlighting context lines too keeps the file's normal
// colour rhythm; without it +/- lines pop while context goes flat grey.
func renderContextLine(line, filePath string) string {
	prefix, content := splitDiffPrefix(line)
	if content == "" {
		return MutedStyle.Render(line)
	}
	return MutedStyle.Render(prefix) + syntax.Highlight(content, filePath)
}

// renderDiffLine renders one diff line as gutter (fg+bg) + body (bg-only with
// highlighted fg), wrapping long content so each row pads to width. Without
// padding, lipgloss stops the bg at the last code char and the band breaks.
// Continuation rows reuse the sigil but blank the line-number column.
func renderDiffLine(line string, gutter, body lipgloss.Style, filePath string, width int) string {
	prefix, content := splitDiffPrefix(line)
	prefixWidth := lipgloss.Width(prefix)
	bodyWidth := width - prefixWidth
	if bodyWidth <= 0 {
		highlighted := syntax.Highlight(content, filePath)
		return gutter.Render(prefix) + body.Render(highlighted)
	}

	segments := wrapVisible(content, bodyWidth)
	if len(segments) == 0 {
		segments = []string{""}
	}

	contPrefix := ""
	if prefixWidth > 0 {
		contPrefix = string(prefix[0]) + strings.Repeat(" ", prefixWidth-1)
	}

	var sb strings.Builder
	for i, seg := range segments {
		p := prefix
		if i > 0 {
			p = contPrefix
		}
		pad := max(bodyWidth-lipgloss.Width(seg), 0)
		highlighted := syntax.Highlight(seg, filePath)
		if pad > 0 {
			highlighted += strings.Repeat(" ", pad)
		}
		sb.WriteString(gutter.Render(p))
		sb.WriteString(body.Render(highlighted))
		if i < len(segments)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// wrapVisible hard-wraps s into chunks of at most width terminal cells,
// rune-aware so wide chars and combining marks aren't split. Hard wrap (no
// word boundary) because diff bodies often hold long unbroken tokens.
func wrapVisible(s string, width int) []string {
	if width <= 0 || s == "" {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	curW := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if rw == 0 {
			cur.WriteRune(r)
			continue
		}
		if curW+rw > width {
			out = append(out, cur.String())
			cur.Reset()
			curW = 0
		}
		cur.WriteRune(r)
		curW += rw
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// renderIntraLineDiff highlights the changed substring within a single-line
// change: unchanged head/tail keep the base bg, the changed middle uses the
// deeper bg. Rune-level diff so multi-byte chars aren't split.
func renderIntraLineDiff(removedLine, addedLine, filePath string, width int) (string, string) {
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

	rem := composeIntraLine(remPrefix, commonPre, remMid, commonSuf, filePath, width,
		DiffRemoveGutterStyle, DiffRemoveBodyStyle, DiffRemoveInverseStyle)
	add := composeIntraLine(addPrefix, commonPre, addMid, commonSuf, filePath, width,
		DiffAddGutterStyle, DiffAddBodyStyle, DiffAddInverseStyle)
	return rem, add
}

// composeIntraLine assembles one side of an intra-line diff: gutter (fg+bg),
// then body split into common-pre / changed-mid / common-suf with the changed
// segment in the deeper bg. Each segment is highlighted independently.
func composeIntraLine(prefix, pre, mid, suf, filePath string, width int, gutter, body, emphasis lipgloss.Style) string {
	used := lipgloss.Width(prefix) + lipgloss.Width(pre) + lipgloss.Width(mid) + lipgloss.Width(suf)
	pad := ""
	if remaining := width - used; remaining > 0 {
		pad = strings.Repeat(" ", remaining)
	}

	var sb strings.Builder
	sb.WriteString(gutter.Render(prefix))
	if pre != "" {
		sb.WriteString(body.Render(syntax.Highlight(pre, filePath)))
	}
	if mid != "" {
		sb.WriteString(emphasis.Render(syntax.Highlight(mid, filePath)))
	}
	if suf != "" || pad != "" {
		sb.WriteString(body.Render(syntax.Highlight(suf, filePath) + pad))
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

// RenderWriteResult renders what the write tool reports. Its diff was shown
// with the call's preview.
func RenderWriteResult(result string) string {
	return ToolIconStyle.Render("✓  ") + MutedStyle.Render(result)
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
func RenderLsResult(result string) (dirPath string, body string) {
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
func RenderReadSummary(result string) string {
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

// RenderGlobResult renders glob results: a path list, its line numbers
// colored when it has any ("  123\tcontent").
func RenderGlobResult(result string) string {
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
