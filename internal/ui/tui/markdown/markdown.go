// Package markdown renders markdown and wraps plain text for the terminal.
// Every line fits the given width and carries its own styling, so any line
// can be shown without the lines before it.
package markdown

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"

	"github.com/voocel/codebot/internal/ui/tui/syntax"
	"github.com/voocel/codebot/internal/ui/tui/theme"
)

var parser = goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()

func Render(src string, width int) []string {
	width = max(width, 8)
	source := []byte(src)
	r := &renderer{src: source}
	return r.blocks(parser.Parse(text.NewReader(source)), width, false)
}

type renderer struct {
	src   []byte
	depth int // list nesting depth
}

func (r *renderer) blocks(n ast.Node, width int, tight bool) []string {
	var out []string
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		lines := r.block(c, width)
		if len(lines) == 0 {
			continue
		}
		if len(out) > 0 && !tight {
			out = append(out, "")
		}
		out = append(out, lines...)
	}
	return out
}

func (r *renderer) block(n ast.Node, width int) []string {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		return layout(r.inline(n, theme.Text), width)
	case *ast.Heading:
		return layout(r.inline(n, headingStyle(n.Level)), width)
	case *ast.ThematicBreak:
		return []string{theme.FaintText.Render(strings.Repeat("─", width))}
	case *ast.FencedCodeBlock:
		return code(r.lines(n), string(n.Language(r.src)), width)
	case *ast.CodeBlock:
		return code(r.lines(n), "", width)
	case *ast.HTMLBlock:
		return Wrap(strings.TrimRight(r.lines(n), "\n"), theme.MutedText, width)
	case *ast.Blockquote:
		bar := theme.SubtleText.Render("▎ ")
		inner := r.blocks(n, width-2, false)
		for i, l := range inner {
			inner[i] = bar + l
		}
		return inner
	case *ast.List:
		return r.list(n, width)
	case *east.Table:
		return r.table(n, width)
	default:
		return r.blocks(n, width, false)
	}
}

func headingStyle(level int) lipgloss.Style {
	switch level {
	case 1:
		return lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
	case 2:
		return lipgloss.NewStyle().Foreground(theme.Strong).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(theme.Fg).Bold(true)
	}
}

func (r *renderer) lines(n ast.Node) string {
	var b strings.Builder
	segs := n.Lines()
	for i := range segs.Len() {
		seg := segs.At(i)
		b.WriteString(strings.Repeat(" ", seg.Padding))
		b.Write(seg.Value(r.src))
	}
	return b.String()
}

var bullets = []string{"•", "◦", "▪"}

func (r *renderer) list(l *ast.List, width int) []string {
	r.depth++
	defer func() { r.depth-- }()

	marker := theme.MutedText
	var out []string
	num := l.Start
	for item := l.FirstChild(); item != nil; item = item.NextSibling() {
		m := bullets[(r.depth-1)%len(bullets)]
		if l.IsOrdered() {
			m = fmt.Sprintf("%d.", num)
			num++
		}
		pad := ansi.StringWidth(m) + 1
		inner := r.blocks(item, width-pad, l.IsTight)
		if len(inner) == 0 {
			inner = []string{""}
		}
		if len(out) > 0 && !l.IsTight {
			out = append(out, "")
		}
		for i, line := range inner {
			if i == 0 {
				out = append(out, marker.Render(m)+" "+line)
			} else {
				out = append(out, strings.Repeat(" ", pad)+line)
			}
		}
	}
	return out
}

func code(src, lang string, width int) []string {
	src = strings.TrimRight(strings.ReplaceAll(src, "\t", "    "), "\n")
	bar := theme.FaintText.Render("│ ")
	var out []string
	for _, line := range strings.Split(syntax.Lang(src, lang), "\n") {
		for _, part := range strings.Split(ansi.Hardwrap(line, width-2, true), "\n") {
			out = append(out, bar+part)
		}
	}
	return out
}

// table falls back to "header: value" lines per row when the grid doesn't
// fit.
func (r *renderer) table(t *east.Table, width int) []string {
	var rows [][][]span
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		var cells [][]span
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			st := theme.Text
			if _, header := row.(*east.TableHeader); header {
				st = theme.Bold
			}
			cells = append(cells, r.inline(cell, st))
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return nil
	}

	cols := len(t.Alignments)
	widths := make([]int, cols)
	for _, row := range rows {
		for i, cell := range row[:min(len(row), cols)] {
			widths[i] = max(widths[i], spansWidth(cell))
		}
	}
	total := 1
	for _, w := range widths {
		total += w + 3
	}
	if total > width {
		return r.verticalTable(rows, width)
	}

	rule := func(l, m, rt string) string {
		parts := make([]string, cols)
		for i, w := range widths {
			parts[i] = strings.Repeat("─", w+2)
		}
		return theme.FaintText.Render(l + strings.Join(parts, m) + rt)
	}
	bar := theme.FaintText.Render("│")
	out := []string{rule("┌", "┬", "┐")}
	for ri, row := range rows {
		var b strings.Builder
		b.WriteString(bar)
		for i := range cols {
			var cell []span
			if i < len(row) {
				cell = row[i]
			}
			b.WriteString(" " + align(renderSpans(cell), widths[i], t.Alignments[i]) + " " + bar)
		}
		out = append(out, b.String())
		if ri == 0 {
			out = append(out, rule("├", "┼", "┤"))
		}
	}
	return append(out, rule("└", "┴", "┘"))
}

func (r *renderer) verticalTable(rows [][][]span, width int) []string {
	header := rows[0]
	var out []string
	for ri, row := range rows[1:] {
		if ri > 0 {
			out = append(out, theme.FaintText.Render(strings.Repeat("─", min(width, 24))))
		}
		for i, cell := range row {
			label := "Column"
			if i < len(header) {
				label = spansText(header[i])
			}
			out = append(out, layout(append([]span{{label + ": ", theme.Bold}}, cell...), width)...)
		}
	}
	return out
}

func align(s string, width int, a east.Alignment) string {
	pad := width - ansi.StringWidth(s)
	switch a {
	case east.AlignRight:
		return strings.Repeat(" ", pad) + s
	case east.AlignCenter:
		return strings.Repeat(" ", pad/2) + s + strings.Repeat(" ", pad-pad/2)
	default:
		return s + strings.Repeat(" ", pad)
	}
}

func (r *renderer) inline(n ast.Node, st lipgloss.Style) []span {
	var out []span
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			out = append(out, span{string(c.Value(r.src)), st})
			// Models break lines expecting them kept, so treat a soft break
			// as a hard one.
			if c.SoftLineBreak() || c.HardLineBreak() {
				out = append(out, span{"\n", st})
			}
		case *ast.String:
			out = append(out, span{string(c.Value), st})
		case *ast.CodeSpan:
			out = append(out, span{r.plain(c), lipgloss.NewStyle().Foreground(theme.Info)})
		case *ast.Emphasis:
			if c.Level >= 2 {
				out = append(out, r.inline(c, st.Bold(true))...)
			} else {
				out = append(out, r.inline(c, st.Italic(true))...)
			}
		case *east.Strikethrough:
			out = append(out, r.inline(c, st.Strikethrough(true))...)
		case *ast.Link:
			label := r.inline(c, st.Foreground(theme.Info).Underline(true))
			out = append(out, label...)
			if dest := string(c.Destination); dest != spansText(label) {
				out = append(out, span{" (" + dest + ")", theme.SubtleText})
			}
		case *ast.AutoLink:
			out = append(out, span{string(c.URL(r.src)), st.Foreground(theme.Info).Underline(true)})
		case *ast.Image:
			out = append(out, span{"[image: " + r.plain(c) + "]", theme.MutedText})
		case *ast.RawHTML:
			var b strings.Builder
			for i := range c.Segments.Len() {
				seg := c.Segments.At(i)
				b.Write(seg.Value(r.src))
			}
			out = append(out, span{b.String(), theme.MutedText})
		case *east.TaskCheckBox:
			if c.IsChecked {
				out = append(out, span{"✓ ", theme.OKText})
			} else {
				out = append(out, span{"☐ ", theme.MutedText})
			}
		default:
			out = append(out, r.inline(c, st)...)
		}
	}
	return out
}

func (r *renderer) plain(n ast.Node) string {
	return spansText(r.inline(n, lipgloss.NewStyle()))
}
