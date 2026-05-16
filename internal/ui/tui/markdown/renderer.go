package markdown

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	ansiRE       = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	headingRE    = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	blockquoteRE = regexp.MustCompile(`^\s*>\s?(.*)$`)
	unorderedRE  = regexp.MustCompile(`^(\s*)[-*+]\s+(.+?)\s*$`)
	orderedRE    = regexp.MustCompile(`^(\s*)(\d+)\.\s+(.+?)\s*$`)
	imageRE      = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	linkRE       = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	codeSpanRE   = regexp.MustCompile("`([^`\n]+)`")
	strongRE     = regexp.MustCompile(`\*\*([^\n]+?)\*\*|__([^\n]+?)__`)
	emphasisRE   = regexp.MustCompile(`\*([^\s*][^*\n]*?)\*|_([^\s_][^_\n]*?)_`)
)

// Renderer renders a lightweight markdown subset for the TUI.
type Renderer struct {
	width int
}

// NewRenderer creates a markdown renderer at the given width.
func NewRenderer(width int) *Renderer {
	r := &Renderer{}
	r.SetWidth(width)
	return r
}

// SetWidth stores the terminal width for future lightweight formatting.
func (r *Renderer) SetWidth(width int) {
	if width <= 0 {
		width = 80
	}
	r.width = width
}

// RenderFinal renders a terminal-friendly markdown subset.
func (r *Renderer) RenderFinal(content string) string {
	if content == "" {
		return ""
	}
	if r == nil {
		return strings.TrimSpace(content)
	}

	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var out []string
	inCodeBlock := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if isFence(trimmed) {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			out = append(out, renderCodeBlock(line))
			continue
		}
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		if isHorizontalRule(trimmed) {
			out = append(out, "---")
			continue
		}
		if m := headingRE.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			out = append(out, renderHeading(level, formatInline(m[2])))
			continue
		}
		if m := blockquoteRE.FindStringSubmatch(line); m != nil {
			out = append(out, renderQuoteBar("│")+" "+renderEmphasis(formatInline(m[1])))
			continue
		}
		if m := unorderedRE.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+renderListMarker("-")+" "+formatInline(m[2]))
			continue
		}
		if m := orderedRE.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+renderListMarker(m[2]+".")+" "+formatInline(m[3]))
			continue
		}
		if isTableHeaderStart(lines, i) {
			block, next := collectTableBlock(lines, i)
			out = append(out, formatTableBlock(block, r.width)...)
			i = next - 1
			continue
		}
		if isTableLine(trimmed) {
			out = append(out, formatTableLine(line))
			continue
		}
		out = append(out, formatInline(line))
	}

	return strings.TrimSpace(strings.Join(out, "\n"))
}

// Width reports the current renderer width.
func (r *Renderer) Width() int {
	if r == nil {
		return 0
	}
	return r.width
}

func isFence(line string) bool {
	return strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
}

func isTableLine(line string) bool {
	return strings.Count(line, "|") >= 2
}

func formatTableLine(line string) string {
	parts := strings.Split(line, "|")
	for i, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			parts[i] = part
			continue
		}
		if isTableSeparator(trimmed) {
			parts[i] = renderSeparator(trimmed)
			continue
		}
		prefix := leadingWhitespace(part)
		suffix := trailingWhitespace(part)
		parts[i] = prefix + formatInline(trimmed) + suffix
	}
	return strings.Join(parts, "|")
}

func isTableHeaderStart(lines []string, start int) bool {
	if start+1 >= len(lines) {
		return false
	}
	header := strings.TrimSpace(lines[start])
	separator := strings.TrimSpace(lines[start+1])
	if !isTableLine(header) || !isTableLine(separator) {
		return false
	}
	return isTableSeparatorRow(parseTableRow(separator))
}

func collectTableBlock(lines []string, start int) ([]string, int) {
	end := start
	for end < len(lines) {
		trimmed := strings.TrimSpace(lines[end])
		if trimmed == "" || !isTableLine(trimmed) {
			break
		}
		end++
	}
	return lines[start:end], end
}

func formatTableBlock(lines []string, width int) []string {
	if len(lines) == 0 {
		return nil
	}

	rows := make([][]string, 0, len(lines))
	var aligns []string
	widths := []int{}
	for i, line := range lines {
		row := parseTableRow(line)
		rows = append(rows, row)
		if i == 1 && isTableSeparatorRow(row) {
			aligns = parseTableAlignments(row)
			continue
		}
		if len(row) > len(widths) {
			widths = append(widths, make([]int, len(row)-len(widths))...)
		}
		for col, cell := range row {
			widths[col] = max(widths[col], textWidth(cell))
		}
	}

	if tableTooWide(width, widths) {
		return formatVerticalTable(rows[0], rows[2:])
	}

	out := make([]string, 0, len(rows)+2)
	out = append(out, formatTableBorder("top", widths))
	out = append(out, formatTableRow(rows[0], widths, aligns, true))
	out = append(out, formatTableBorder("middle", widths))
	for rowIndex, row := range rows[2:] {
		out = append(out, formatTableRow(row, widths, aligns, false))
		if rowIndex < len(rows[2:])-1 {
			out = append(out, formatTableBorder("middle", widths))
		}
	}
	out = append(out, formatTableBorder("bottom", widths))
	return out
}

func parseTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}
	return parts
}

func isTableSeparator(cell string) bool {
	cell = strings.Trim(cell, ":")
	if cell == "" {
		return false
	}
	for _, r := range cell {
		if r != '-' {
			return false
		}
	}
	return true
}

func isTableSeparatorRow(row []string) bool {
	if len(row) == 0 {
		return false
	}
	for _, cell := range row {
		if !isTableSeparator(cell) {
			return false
		}
	}
	return true
}

func parseTableAlignments(row []string) []string {
	aligns := make([]string, len(row))
	for i, cell := range row {
		trimmed := strings.TrimSpace(cell)
		left := strings.HasPrefix(trimmed, ":")
		right := strings.HasSuffix(trimmed, ":")
		switch {
		case left && right:
			aligns[i] = "center"
		case right:
			aligns[i] = "right"
		default:
			aligns[i] = "left"
		}
	}
	return aligns
}

func isHorizontalRule(line string) bool {
	line = strings.ReplaceAll(line, " ", "")
	if len(line) < 3 {
		return false
	}
	marker := line[0]
	if marker != '-' && marker != '*' && marker != '_' {
		return false
	}
	for i := 1; i < len(line); i++ {
		if line[i] != marker {
			return false
		}
	}
	return true
}

func formatInline(text string) string {
	text = replaceAllStringSubmatchFunc(imageRE, text, func(groups []string) string {
		if strings.TrimSpace(groups[1]) != "" {
			return renderLink(groups[1]) + " (" + groups[2] + ")"
		}
		return groups[2]
	})
	text = replaceAllStringSubmatchFunc(linkRE, text, func(groups []string) string {
		label := strings.TrimSpace(groups[1])
		if label == "" || label == groups[2] {
			return renderLink(groups[2])
		}
		return renderLink(label) + " (" + groups[2] + ")"
	})

	placeholders := map[string]string{}
	nextPlaceholder := func(rendered string) string {
		key := "\x00" + strings.Repeat("x", len(placeholders)+1) + "\x00"
		placeholders[key] = rendered
		return key
	}

	text = replaceAllStringSubmatchFunc(codeSpanRE, text, func(groups []string) string {
		return nextPlaceholder(renderCodeSpan(groups[1]))
	})
	text = replaceAllStringSubmatchFunc(strongRE, text, func(groups []string) string {
		value := firstNonEmpty(groups[1], groups[2])
		return nextPlaceholder(renderStrong(value))
	})
	text = replaceAllStringSubmatchFunc(emphasisRE, text, func(groups []string) string {
		value := firstNonEmpty(groups[1], groups[2])
		return nextPlaceholder(renderEmphasis(value))
	})

	for key, rendered := range placeholders {
		text = strings.ReplaceAll(text, key, rendered)
	}
	return text
}

func replaceAllStringSubmatchFunc(re *regexp.Regexp, src string, fn func([]string) string) string {
	matches := re.FindAllStringSubmatchIndex(src, -1)
	if len(matches) == 0 {
		return src
	}

	var b strings.Builder
	last := 0
	for _, match := range matches {
		b.WriteString(src[last:match[0]])
		groups := make([]string, 0, len(match)/2)
		for i := 0; i < len(match); i += 2 {
			if match[i] < 0 || match[i+1] < 0 {
				groups = append(groups, "")
				continue
			}
			groups = append(groups, src[match[i]:match[i+1]])
		}
		b.WriteString(fn(groups))
		last = match[1]
	}
	b.WriteString(src[last:])
	return b.String()
}

func leadingWhitespace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

func trailingWhitespace(s string) string {
	return s[len(strings.TrimRight(s, " \t")):]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func ansiWrap(prefix, text string) string {
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\x1b[0m", "\x1b[0m"+prefix)
	return prefix + text + "\x1b[0m"
}

func ansiPrefix(attrs ...string) string {
	return "\x1b[" + strings.Join(attrs, ";") + "m"
}

func ansiColorPrefix(light, dark string, attrs ...string) string {
	color := light
	if lipgloss.HasDarkBackground() {
		color = dark
	}
	codes := append(append([]string{}, attrs...), "38", "5", color)
	return ansiPrefix(codes...)
}

func headingPrefix(level int) string {
	switch level {
	case 1:
		return ansiPrefix("1", "3", "4")
	case 2:
		return ansiPrefix("1")
	case 3, 4, 5, 6:
		return ansiPrefix("1")
	default:
		return ansiPrefix("1")
	}
}

func renderHeading(level int, text string) string {
	return ansiWrap(headingPrefix(level), text)
}

func formatTableRow(row []string, widths []int, aligns []string, header bool) string {
	cells := make([]string, len(widths))
	for i := range widths {
		var cell string
		if i < len(row) {
			cell = formatInline(row[i])
		}
		align := "left"
		if header {
			align = "center"
		} else if i < len(aligns) && aligns[i] != "" {
			align = aligns[i]
		}
		cells[i] = padAlignedVisible(cell, widths[i], align)
	}
	sep := renderSeparator("│")
	return sep + " " + strings.Join(cells, " "+sep+" ") + " " + sep
}

func formatTableBorder(kind string, widths []int) string {
	left, mid, right := "┌", "┬", "┐"
	switch kind {
	case "middle":
		left, mid, right = "├", "┼", "┤"
	case "bottom":
		left, mid, right = "└", "┴", "┘"
	}
	segments := make([]string, len(widths))
	for i, width := range widths {
		segments[i] = strings.Repeat("─", max(width+2, 3))
	}
	return renderSeparator(left + strings.Join(segments, mid) + right)
}

func formatVerticalTable(header []string, rows [][]string) []string {
	var headers []string
	for _, cell := range header {
		headers = append(headers, stripANSI(formatInline(cell)))
	}
	var out []string
	for rowIndex, row := range rows {
		if rowIndex > 0 {
			out = append(out, renderSeparator(strings.Repeat("─", 24)))
		}
		for colIndex, label := range headers {
			if label == "" {
				label = "Column"
			}
			var value string
			if colIndex < len(row) {
				value = formatInline(row[colIndex])
			}
			out = append(out, renderStrong(label+":")+" "+value)
		}
	}
	return out
}

func tableTooWide(width int, columns []int) bool {
	if width <= 0 {
		return false
	}
	total := 1
	for _, col := range columns {
		total += col + 3
	}
	return total > max(width-4, 20)
}

func padAlignedVisible(text string, width int, align string) string {
	padding := width - lipgloss.Width(text)
	if padding <= 0 {
		return text
	}
	switch align {
	case "center":
		left := padding / 2
		right := padding - left
		return strings.Repeat(" ", left) + text + strings.Repeat(" ", right)
	case "right":
		return strings.Repeat(" ", padding) + text
	default:
		return text + strings.Repeat(" ", padding)
	}
}

func stripANSI(text string) string {
	return ansiRE.ReplaceAllString(text, "")
}

func textWidth(text string) int {
	return lipgloss.Width(stripANSI(text))
}

func renderCodeBlock(text string) string {
	return text
}

func renderCodeSpan(text string) string {
	return ansiWrap(ansiColorPrefix("26", "111"), text)
}

func renderStrong(text string) string {
	return ansiWrap(ansiPrefix("1"), text)
}

func renderEmphasis(text string) string {
	return ansiWrap(ansiPrefix("3"), text)
}

func renderQuoteBar(text string) string {
	return ansiWrap(ansiPrefix("2"), text)
}

func renderListMarker(text string) string {
	return text
}

func renderSeparator(text string) string {
	return text
}

func renderLink(text string) string {
	return ansiWrap(ansiColorPrefix("26", "111"), text)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
