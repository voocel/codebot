package tui

// Tool rendering primitives:
//   - Header: display name + short summary from tool args.
//   - Output: tree-connector list, path-token highlighting, streaming tail.
//   - Progress: structured ProgressPayload formatting (used by subagent updates).
//
// Per-tool result renderers (edit/write/ls/read/subagent) live in render_tool_results.go.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
)

// IsHiddenTool reports whether a tool's invocation should be omitted from
// the visible TUI stream (live events and session restore alike).
//
// task_* tools manage shared coordination state for the agent's own
// bookkeeping, not work the user wants to follow turn-by-turn. SubAgent
// dispatch and other execution-unit tools stay visible because they signal
// real progress.
func IsHiddenTool(tool string) bool {
	switch tool {
	case "task_create", "task_update", "task_get", "task_list":
		return true
	}
	return false
}

// IsHiddenToolCall extends tool-level hiding with call-specific internal
// filesystem paths. Auto-memory reads are system context hydration, like
// AGENTS.md loading, so their ENOENT/success output should not enter the user
// transcript.
func IsHiddenToolCall(tool string, args json.RawMessage) bool {
	if IsHiddenTool(tool) {
		return true
	}
	if tool != "read" {
		return false
	}
	return isAutoMemoryPath(extractPathArg(args))
}

func extractPathArg(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal(args, &obj) != nil {
		return ""
	}
	path, _ := obj["path"].(string)
	return path
}

func isAutoMemoryPath(path string) bool {
	if path == "" {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	return strings.Contains(clean, "/memory/") &&
		(strings.Contains(clean, "/.codebot/projects/") || strings.HasPrefix(clean, "~/.codebot/projects/"))
}

// ---------------------------------------------------------------------------
// Header
// ---------------------------------------------------------------------------

// toolDisplayName returns a capitalized display name for a tool.
func toolDisplayName(tool string) string {
	names := map[string]string{
		"bash": "Bash", "read": "Read", "edit": "Edit", "write": "Write",
		"grep": "Grep", "glob": "Glob", "ls": "Ls",
	}
	if n, ok := names[tool]; ok {
		return n
	}
	if tool == "" {
		return tool
	}
	parts := strings.FieldsFunc(tool, func(r rune) bool {
		return r == '_' || r == '-'
	})
	if len(parts) == 0 {
		return tool
	}
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		if len(part) > 1 {
			b.WriteString(part[1:])
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	return tool
}

// extractToolSummary extracts a human-readable summary from tool args.
func extractToolSummary(tool string, args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal(args, &obj) != nil {
		return ""
	}
	switch tool {
	case "bash":
		if v, ok := obj["command"].(string); ok && v != "" {
			return v
		}
	case "task_create":
		if v, ok := obj["subject"].(string); ok && v != "" {
			return v
		}
	case "task_get":
		if v, ok := obj["taskId"].(string); ok && v != "" {
			return "#" + v
		}
	case "task_update":
		var parts []string
		if v, ok := obj["taskId"].(string); ok && v != "" {
			parts = append(parts, "#"+v)
		}
		if v, ok := obj["status"].(string); ok && v != "" {
			parts = append(parts, v)
		}
		if v, ok := obj["subject"].(string); ok && v != "" {
			parts = append(parts, v)
		}
		return strings.Join(parts, " ")
	case "read", "edit", "write":
		if v, ok := obj["path"].(string); ok && v != "" {
			return ShortenPath(v)
		}
	case "ls":
		if v, ok := obj["path"].(string); ok && v != "" {
			return ShortenPath(v)
		}
		return "."
	case "grep":
		if v, ok := obj["pattern"].(string); ok && v != "" {
			return v
		}
	case "glob":
		if v, ok := obj["pattern"].(string); ok && v != "" {
			return v
		}
	default:
		// Use the first string value found.
		for _, v := range obj {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// FormatToolHeader returns a one-line tool header like "Bash(git diff --stat)".
func FormatToolHeader(tool string, args json.RawMessage) string {
	name := toolDisplayName(tool)
	summary := extractToolSummary(tool, args)
	if summary != "" {
		return name + "(" + truncateRunes(summary, 60) + ")"
	}
	return name
}

// RenderToolHeader styles the tool name while keeping the summary muted.
func RenderToolHeader(tool string, args json.RawMessage) string {
	name := toolDisplayName(tool)
	summary := extractToolSummary(tool, args)
	if summary == "" {
		return ToolNameStyle.Render(name)
	}
	return ToolNameStyle.Render(name) + ToolArgsStyle.Render("("+truncateRunes(summary, 60)+")")
}

// ---------------------------------------------------------------------------
// Output (generic tool result body)
// ---------------------------------------------------------------------------

// FormatToolOutput formats tool result text with tree connectors.
// First line gets the TreeConnector, subsequent lines get ConnectorPad alignment.
// Truncates to maxVisible lines with "… +N lines" hint.
// Optional styles override the default ToolResultStyle for line content.
func FormatToolOutput(text string, maxVisible int, styles ...lipgloss.Style) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return ""
	}
	lineStyle := ToolResultStyle
	if len(styles) > 0 {
		lineStyle = styles[0]
	}
	lines := strings.Split(text, "\n")
	hidden := 0
	if maxVisible > 0 && len(lines) > maxVisible {
		hidden = len(lines) - maxVisible
		lines = lines[:maxVisible]
	}
	connector := ConnectorStyle.Render(TreeConnector)
	var sb strings.Builder
	for i, line := range lines {
		if i == 0 {
			sb.WriteString(connector)
		} else {
			sb.WriteString(ConnectorPad)
		}
		sb.WriteString(renderToolOutputLine(line, lineStyle))
		sb.WriteByte('\n')
	}
	if hidden > 0 {
		sb.WriteString(MutedStyle.Render(fmt.Sprintf("%s… +%d lines", ConnectorPad, hidden)))
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderToolOutputLine(line string, base lipgloss.Style) string {
	if line == "" {
		return ""
	}
	if styled, ok := renderDiffStatLine(line, base); ok {
		return styled
	}

	parts := strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t'
	})
	if len(parts) == 0 {
		return base.Render(line)
	}

	var out strings.Builder
	for i := 0; i < len(line); {
		if line[i] == ' ' || line[i] == '\t' {
			out.WriteByte(line[i])
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] != ' ' && line[j] != '\t' {
			j++
		}
		token := line[i:j]
		out.WriteString(renderToolOutputToken(token, base))
		i = j
	}
	return out.String()
}

func renderDiffStatLine(line string, base lipgloss.Style) (string, bool) {
	sep := strings.Index(line, " | ")
	if sep <= 0 {
		return "", false
	}
	left := strings.TrimRight(line[:sep], " \t")
	right := line[sep:]
	trimmedLeft := strings.TrimLeft(left, " \t")
	if !looksLikePathToken(trimmedLeft) {
		return "", false
	}
	prefix := left[:len(left)-len(trimmedLeft)]
	return base.Render(prefix) + ToolPathStyle.Render(trimmedLeft) + base.Render(right), true
}

func renderToolOutputToken(token string, base lipgloss.Style) string {
	trimmed := strings.Trim(token, "[](){}<>\",'\"")
	if looksLikePathToken(trimmed) {
		start := strings.Index(token, trimmed)
		end := start + len(trimmed)
		return base.Render(token[:start]) + ToolPathStyle.Render(trimmed) + base.Render(token[end:])
	}
	return base.Render(token)
}

func looksLikePathToken(token string) bool {
	if token == "" {
		return false
	}
	if strings.HasPrefix(token, "/") || strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") || strings.HasPrefix(token, "~/") {
		return true
	}
	if strings.HasSuffix(token, "/") && token != "/" {
		return true
	}
	if strings.Contains(token, "/") {
		return true
	}
	ext := filepath.Ext(token)
	if ext != "" && len(ext) > 1 && !strings.Contains(token, ":") {
		return true
	}
	return false
}

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

// FormatToolResult extracts displayable text from a tool result.
// Truncation is handled by the caller (FormatToolOutput).
func FormatToolResult(result json.RawMessage, isError bool) string {
	prefix := ""
	if isError {
		prefix = "error: "
	}
	if len(result) == 0 {
		return prefix + "(no output)"
	}

	// Extract "message" or "output" from JSON objects for cleaner display.
	var obj map[string]any
	if json.Unmarshal(result, &obj) == nil {
		if msg, ok := obj["message"].(string); ok && msg != "" {
			return prefix + strings.TrimSpace(msg)
		}
		if out, ok := obj["output"].(string); ok && out != "" {
			return prefix + strings.TrimSpace(out)
		}
	}

	// Try plain JSON string (e.g. "file1\nfile2").
	var str string
	if json.Unmarshal(result, &str) == nil {
		return prefix + strings.TrimSpace(str)
	}

	return prefix + strings.TrimSpace(string(result))
}

// TruncateLines truncates text to maxLines, appending "..." if truncated.
func TruncateLines(s string, maxLines int) string {
	lines := strings.SplitN(s, "\n", maxLines+1)
	if len(lines) > maxLines {
		return strings.Join(lines[:maxLines], "\n") + "..."
	}
	return s
}

// RenderStreamingOutput shows the last N lines of streaming tool output
// with tree connectors (TreeConnector on first line, ConnectorPad for alignment).
func RenderStreamingOutput(full string, maxLines int) string {
	all := strings.TrimRight(full, "\n")
	lines := strings.Split(all, "\n")
	start := max(len(lines)-maxLines, 0)
	visible := lines[start:]
	connector := ConnectorStyle.Render(TreeConnector)
	var sb strings.Builder
	if start > 0 {
		sb.WriteString(connector + MutedStyle.Render(fmt.Sprintf("… +%d lines above", start)))
		sb.WriteByte('\n')
	}
	for i, line := range visible {
		if i == 0 && start == 0 {
			sb.WriteString(connector)
		} else {
			sb.WriteString(ConnectorPad)
		}
		sb.WriteString(MutedStyle.Render(line))
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ---------------------------------------------------------------------------
// Progress (structured subagent updates)
// ---------------------------------------------------------------------------

// FormatProgressLine formats a structured tool progress update for display.
func FormatProgressLine(progress *agentcore.ProgressPayload) string {
	if progress == nil {
		return ""
	}
	switch progress.Kind {
	case agentcore.ProgressToolStart, agentcore.ProgressTurnCounter, agentcore.ProgressToolError:
		return formatSubagentProgress(progress.Tool, progress.Args, progress.Turn, progress.IsError)
	case agentcore.ProgressRetry:
		if progress.Attempt > 0 && progress.MaxRetries > 0 {
			return MutedStyle.Render(fmt.Sprintf("retry %d/%d", progress.Attempt, progress.MaxRetries))
		}
		if progress.Message != "" {
			return truncateRunes(progress.Message, 200)
		}
	case agentcore.ProgressSummary:
		if progress.Summary != "" {
			return truncateRunes(progress.Summary, 200)
		}
	}
	if progress.Summary != "" {
		return truncateRunes(progress.Summary, 200)
	}
	if progress.Message != "" {
		return truncateRunes(progress.Message, 200)
	}
	if progress.Tool != "" {
		return formatSubagentProgress(progress.Tool, progress.Args, progress.Turn, progress.IsError)
	}
	return ""
}

// formatSubagentProgress renders a structured subagent progress line.
func formatSubagentProgress(tool string, args json.RawMessage, turn int, isError bool) string {
	if turn > 0 {
		return MutedStyle.Render(fmt.Sprintf("turn %d completed", turn))
	}
	if isError {
		return ToolNameStyle.Render(tool) + MutedStyle.Render(" failed")
	}
	if tool != "" {
		line := ToolNameStyle.Render(tool)
		if hint := toolArgHint(args); hint != "" {
			line += MutedStyle.Render(" " + hint)
		}
		return line
	}
	return ""
}

// toolArgHint extracts a short hint from tool args for display.
func toolArgHint(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	// Try common field names in priority order.
	for _, key := range []string{"file_path", "path", "pattern", "glob", "command"} {
		raw, ok := m[key]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return truncateRunes(s, 60)
		}
	}
	return ""
}
