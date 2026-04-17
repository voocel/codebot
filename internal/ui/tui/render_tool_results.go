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

// ---------------------------------------------------------------------------
// Filesystem (write / ls / read)
// ---------------------------------------------------------------------------

// RenderWriteResult renders the write tool result with a green preview of the written content.
func RenderWriteResult(result json.RawMessage) string {
	if len(result) == 0 {
		return "(file written)"
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		return TruncateLines(string(result), 10)
	}

	msg, _ := parsed["message"].(string)
	preview, _ := parsed["preview"].(string)
	if preview == "" {
		return msg
	}

	var sb strings.Builder
	sb.WriteString(msg + "\n")
	for _, line := range strings.Split(strings.TrimRight(preview, "\n"), "\n") {
		if strings.HasPrefix(line, "+") {
			sb.WriteString(DiffAddStyle.Render(line) + "\n")
		} else {
			sb.WriteString(MutedStyle.Render(line) + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
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

	connector := MutedStyle.Render("└ ")
	padding := "  "
	contentStyle := lipgloss.NewStyle().Foreground(ColorText)

	var sb strings.Builder
	for i, line := range remaining {
		if i == 0 {
			sb.WriteString(connector)
		} else {
			sb.WriteString(padding)
		}
		sb.WriteString(renderToolOutputLine(line, contentStyle))
		sb.WriteByte('\n')
	}
	if hidden > 0 {
		sb.WriteString(MutedStyle.Render(fmt.Sprintf("  … +%d lines", hidden)))
		sb.WriteByte('\n')
	}
	return dirPath, strings.TrimRight(sb.String(), "\n")
}

// RenderReadResult renders read/glob tool results with colored line numbers.
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

	connector := MutedStyle.Render("└ ")
	padding := "  "
	lineNumStyle := lipgloss.NewStyle().Foreground(ColorToolMeta)
	contentStyle := lipgloss.NewStyle().Foreground(ColorText)

	var sb strings.Builder
	for i, line := range lines {
		if i == 0 {
			sb.WriteString(connector)
		} else {
			sb.WriteString(padding)
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
		sb.WriteString(MutedStyle.Render(fmt.Sprintf("  … +%d lines", hidden)))
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
