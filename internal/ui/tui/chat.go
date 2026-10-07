package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// chatView renders only visible cells and caches them until they change.
// Its scroll position is a line within a cell, not within the whole
// transcript, so it never needs the height of off-screen cells.
type chatView struct {
	cells func() []transcript.Cell
	empty func(width, height int) []string

	width, height int
	params        transcript.Params // Width is ignored; the view's own is used

	follow bool // stick to the bottom as cells arrive
	top    pos  // first line shown, when not following
	seen   int  // cell count when the view left the bottom

	cache map[transcript.Cell]*rendered
	rows  []pos // the position each row of the last view showed
	sel   selection
}

// pos is a line of a cell. The line after its last is the gap below it.
type pos struct{ cell, line int }

func (a pos) before(b pos) bool { return a.cell < b.cell || a.cell == b.cell && a.line < b.line }

type rendered struct {
	version  uint64
	width    int
	expanded bool
	lines    []string
}

type selection struct {
	on       bool
	dragging bool
	from, to point
}

type point struct {
	pos
	col int
}

func newChatView(cells func() []transcript.Cell, empty func(int, int) []string) *chatView {
	return &chatView{cells: cells, empty: empty, follow: true, cache: map[transcript.Cell]*rendered{}}
}

// lines includes the gap below the cell.
func (v *chatView) lines(cells []transcript.Cell, i int) []string {
	c := cells[i]
	p := v.params
	p.Width = v.width
	var lines []string
	if r := v.cache[c]; !c.Live() && r != nil && r.version == c.Version() && r.width == p.Width && r.expanded == p.Expanded {
		lines = r.lines
	} else {
		lines = c.Render(p)
		for j, l := range lines {
			if ansi.StringWidth(l) > p.Width {
				lines[j] = ansi.Truncate(l, p.Width, "…")
			}
		}
		if !c.Live() {
			v.cache[c] = &rendered{c.Version(), p.Width, p.Expanded, lines}
		}
	}
	if i+1 < len(cells) {
		if a, ok := cells[i+1].(transcript.Attached); !ok || !a.Attached() {
			return append(lines[:len(lines):len(lines)], "")
		}
	}
	return lines
}

// bottom is the top position that puts the last line at the bottom.
func (v *chatView) bottom(cells []transcript.Cell) pos {
	need := v.height
	for i := len(cells) - 1; i >= 0; i-- {
		h := len(v.lines(cells, i))
		if h >= need {
			return pos{i, h - need}
		}
		need -= h
	}
	return pos{}
}

func (v *chatView) view() []string {
	cells := v.cells()
	v.rows = v.rows[:0]
	if len(cells) == 0 {
		return v.empty(v.width, v.height)
	}
	v.settle(cells)
	if !v.follow && !v.top.before(v.bottom(cells)) {
		v.follow = true
	}
	if v.follow {
		v.top = v.bottom(cells)
	}
	var out []string
	for p := v.top; len(out) < v.height && p.cell < len(cells); p = (pos{p.cell + 1, 0}) {
		lines := v.lines(cells, p.cell)
		for ; p.line < len(lines) && len(out) < v.height; p.line++ {
			out = append(out, v.highlight(lines[p.line], p))
			v.rows = append(v.rows, p)
		}
	}
	return out
}

// settle keeps top valid after its cell collapsed, rewrapped or vanished.
func (v *chatView) settle(cells []transcript.Cell) {
	if v.follow {
		return
	}
	if v.top.cell >= len(cells) {
		v.follow = true
		return
	}
	if v.top.line >= len(v.lines(cells, v.top.cell)) {
		v.top.line = 0
	}
}

func (v *chatView) scroll(n int) {
	cells := v.cells()
	if len(cells) == 0 {
		return
	}
	v.settle(cells)
	if v.follow {
		v.top, v.seen = v.bottom(cells), len(cells)
	}
	p := v.top
	for ; n < 0; n++ {
		if p.line == 0 {
			if p.cell == 0 {
				break
			}
			p.cell--
			p.line = len(v.lines(cells, p.cell))
		}
		p.line--
	}
	for n > 0 && p.cell < len(cells) {
		h := len(v.lines(cells, p.cell))
		if p.line+n < h {
			p.line += n
			break
		}
		n -= h - p.line
		p = pos{p.cell + 1, 0}
	}
	v.top = p
	v.follow = !p.before(v.bottom(cells))
}

func (v *chatView) toTop() {
	cells := v.cells()
	v.top, v.seen = pos{}, len(cells)
	v.follow = !v.top.before(v.bottom(cells))
}

func (v *chatView) toBottom() { v.follow = true }

func (v *chatView) unseen() int {
	if v.follow {
		return 0
	}
	return max(len(v.cells())-v.seen, 0)
}

func (v *chatView) at(y int) (pos, bool) {
	if y < 0 || y >= len(v.rows) {
		return pos{}, false
	}
	return v.rows[y], true
}

func (v *chatView) cell(p pos) transcript.Cell {
	cells := v.cells()
	if p.cell >= len(cells) {
		return nil
	}
	return cells[p.cell]
}

func (v *chatView) press(x, y int) {
	p, ok := v.at(y)
	if !ok {
		v.sel = selection{}
		return
	}
	pt := point{p, max(x, 0)}
	v.sel = selection{on: true, dragging: true, from: pt, to: pt}
}

func (v *chatView) drag(x, y int) {
	if !v.sel.dragging {
		return
	}
	y = min(max(y, 0), len(v.rows)-1)
	if p, ok := v.at(y); ok {
		v.sel.to = point{p, max(x, 0)}
	}
}

// release returns "" for a click, which clears the selection.
func (v *chatView) release() string {
	if !v.sel.dragging {
		return ""
	}
	v.sel.dragging = false
	if v.sel.from == v.sel.to {
		v.sel = selection{}
		return ""
	}
	return v.selected()
}

func (v *chatView) clearSelection() { v.sel = selection{} }

func (s selection) span() (point, point) {
	if s.to.pos.before(s.from.pos) || s.to.pos == s.from.pos && s.to.col < s.from.col {
		return s.to, s.from
	}
	return s.from, s.to
}

func (v *chatView) cols(p pos) (from, to int, ok bool) {
	if !v.sel.on {
		return 0, 0, false
	}
	a, b := v.sel.span()
	if p.before(a.pos) || b.pos.before(p) {
		return 0, 0, false
	}
	from, to = 0, v.width
	if p == a.pos {
		from = a.col
	}
	if p == b.pos {
		to = b.col + 1
	}
	return from, to, from < to
}

var selectionStyle = func() lipgloss.Style { return lipgloss.NewStyle().Background(theme.Selection).Foreground(theme.Strong) }

func (v *chatView) highlight(line string, p pos) string {
	from, to, ok := v.cols(p)
	if !ok {
		return line
	}
	w := ansi.StringWidth(line)
	if from >= w {
		return line
	}
	to = min(to, w)
	return ansi.Cut(line, 0, from) + selectionStyle().Render(ansi.Strip(ansi.Cut(line, from, to))) + ansi.Cut(line, to, w)
}

func (v *chatView) selected() string {
	cells := v.cells()
	a, b := v.sel.span()
	var out []string
	for p := a.pos; !b.pos.before(p) && p.cell < len(cells); p = (pos{p.cell + 1, 0}) {
		lines := v.lines(cells, p.cell)
		for ; p.line < len(lines) && !b.pos.before(p); p.line++ {
			from, to, _ := v.cols(p)
			out = append(out, strings.TrimRight(ansi.Cut(ansi.Strip(lines[p.line]), from, to), " "))
		}
	}
	return tidy(out, a.col > 0)
}

var marks = []string{"●", "⎿", "❯", "│", "▎", "✻", "▸", "✓", "!"}

var bullets = []string{"•", "◦", "▪"}

// tidy blanks leading marks, turns bullets back into markdown and strips the
// shared indent. A first line the selection cut has lost its indent, so it
// doesn't count.
func tidy(lines []string, cut bool) string {
	indent := -1
	for i, l := range lines {
		if i == 0 && cut {
			lines[i] = strings.TrimLeft(l, " ")
			continue
		}
		l = unmark(l)
		lines[i] = l
		if strings.TrimSpace(l) != "" {
			n := len(l) - len(strings.TrimLeft(l, " "))
			if indent < 0 || n < indent {
				indent = n
			}
		}
	}
	for i, l := range lines {
		if (i > 0 || !cut) && indent > 0 && len(l) >= indent {
			lines[i] = l[indent:]
		}
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

func unmark(l string) string {
	text := strings.TrimLeft(l, " ")
	pad := l[:len(l)-len(text)]
	for _, m := range marks {
		if rest, ok := strings.CutPrefix(text, m+" "); ok {
			pad += strings.Repeat(" ", ansi.StringWidth(m)+1)
			text = rest
			break
		}
	}
	for _, b := range bullets {
		if rest, ok := strings.CutPrefix(text, b+" "); ok {
			return pad + "- " + rest
		}
	}
	return pad + text
}
