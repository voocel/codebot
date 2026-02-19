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
	detail := m.ModelName
	if m.Cwd != "" {
		detail += " · " + shortenPath(m.Cwd)
		if m.GitBranch != "" {
			detail += " (" + m.GitBranch + ")"
		}
	}
	return "\n  " + title + "\n  " + WelcomeDetailStyle.Render(detail)
}

// RenderStatusBar renders the top status bar.
func (m *Model) RenderStatusBar() string {
	var status string
	if m.Running {
		status = m.Spinner.View() + " running"
	} else {
		status = lipgloss.NewStyle().Foreground(ColorSuccess).Render("●") + " ready"
	}

	right := m.StatusInfo()
	if right != "" {
		status += "  " + right
	}

	if m.Width > 0 {
		status = truncate.StringWithTail(status, uint(max(m.Width-2, 1)), "…")
	}
	return status
}

// RenderFooter renders the optional footer bar.
func (m *Model) RenderFooter() string {
	if m.config.OnFooter == nil {
		return ""
	}
	content := m.config.OnFooter(m)
	if content == "" {
		return ""
	}
	return FooterStyle.Width(m.Width).Render(content)
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
	return MutedStyle.Render(fmt.Sprintf("  ─ %d turns · %d tools · ↑%s ↓%s tokens",
		s.Turns, s.ToolCalls, formatTokens(s.Input), formatTokens(s.Output)))
}

// formatTokens formats a token count with k/M suffix for readability.
func formatTokens(n int) string {
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
	s := string(args)
	if len(s) > 100 {
		s = s[:97] + "..."
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
	s := strings.TrimSpace(string(result))
	lines := strings.SplitN(s, "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
		s = strings.Join(lines, "\n") + "\n..."
	}
	if len(s) > 300 {
		s = s[:297] + "..."
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

	var sb strings.Builder
	sb.WriteString(msg + "\n")
	for _, line := range strings.Split(diff, "\n") {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "-"):
			sb.WriteString(DiffRemoveStyle.Render(line))
		case strings.HasPrefix(line, "+"):
			sb.WriteString(DiffAddStyle.Render(line))
		default:
			sb.WriteString(MutedStyle.Render(line))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// FormatProgressLine formats a tool progress update for display.
func FormatProgressLine(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	s := string(result)
	if len(s) > 200 {
		s = s[:197] + "..."
	}
	return s
}
