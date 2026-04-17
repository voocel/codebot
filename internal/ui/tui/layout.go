package tui

// Shared layout utilities: indentation, wrapping, box drawing, formatters.

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	reflowwrap "github.com/muesli/reflow/wrap"
)

// wrapTextForIndent wraps content to fit terminal width after indentation.
func (m *Model) wrapTextForIndent(content string, indent int) string {
	if content == "" {
		return ""
	}
	width := m.Width - indent - 1
	if width <= 1 {
		width = 79
	}
	return strings.TrimRight(reflowwrap.String(content, width), "\n")
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

// formatDuration formats a duration as "Xs", "Xm Xs", or "Xh Xm".
func formatDuration(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, m)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-3]) + "..."
}

// truncateToWidth truncates a string to fit within maxWidth visual cells.
func truncateToWidth(s string, maxWidth int) string {
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > maxWidth {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

// DrawBox draws a rounded border box with fixed height and gray border.
// innerWidth is the content width; fixedRows is the exact number of content rows
// (short content is padded with empty lines to keep view height stable).
func DrawBox(lines []string, innerWidth, fixedRows int) string {
	bs := BoxBorderStyle
	padWidth := innerWidth + 2 // 1 padding each side

	emptyLine := bs.Render("│") + " " + strings.Repeat(" ", innerWidth) + " " + bs.Render("│")

	var b strings.Builder
	b.WriteString(bs.Render("╭" + strings.Repeat("─", padWidth) + "╮"))
	b.WriteByte('\n')

	for i := range fixedRows {
		if i < len(lines) {
			line := lines[i]
			vis := lipgloss.Width(line)
			if vis > innerWidth {
				line = truncateToWidth(line, innerWidth)
				vis = lipgloss.Width(line)
			}
			pad := max(innerWidth-vis, 0)
			b.WriteString(bs.Render("│") + " " + line + strings.Repeat(" ", pad) + " " + bs.Render("│"))
		} else {
			b.WriteString(emptyLine)
		}
		b.WriteByte('\n')
	}

	b.WriteString(bs.Render("╰" + strings.Repeat("─", padWidth) + "╯"))
	return b.String()
}

// FormatTokens formats a token count with k/M suffix for readability.
// Uses floor truncation at 0.1 precision so the displayed number never
// overstates the real value (e.g. 1,050,000 → "1M", not "1.1M"). A trailing
// ".0" is omitted so whole values render as "200k" / "1M" instead of "200.0k".
func FormatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return formatTrimmed(math.Floor(float64(n)/100_000)/10, "M")
	case n >= 1_000:
		return formatTrimmed(math.Floor(float64(n)/100)/10, "k")
	default:
		return fmt.Sprintf("%d", n)
	}
}

func formatTrimmed(v float64, suffix string) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%d%s", int(v), suffix)
	}
	return fmt.Sprintf("%.1f%s", v, suffix)
}
