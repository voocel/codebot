// Package brand draws codebot's mark and the card the welcome and setup
// screens sit in.
package brand

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// Full blocks are painted as background color, since terminals leave no
// gaps between rows in a background.
var bot = [4]string{
	"  ▀▄    ▄▀  ",
	" ▄████████▄ ",
	" ██  ██  ██ ",
	" ▀████████▀ ",
}

// BotWidth is the mark's width in columns.
var BotWidth = ansi.StringWidth(bot[0])

// Bot returns the mark, shaded from the accent to the info color.
func Bot() []string {
	ramp := lipgloss.Blend1D(BotWidth+2*len(bot), theme.Accent, theme.Info)
	out := make([]string, len(bot))
	for y, row := range bot {
		var b strings.Builder
		for x, r := range []rune(row) {
			c := ramp[x+2*y] // a row is about two columns tall
			switch r {
			case ' ':
				b.WriteByte(' ')
			case '█':
				b.WriteString(lipgloss.NewStyle().Background(c).Render(" "))
			default:
				b.WriteString(lipgloss.NewStyle().Foreground(c).Render(string(r)))
			}
		}
		out[y] = b.String()
	}
	return out
}

// Beside puts lines beside the mark, below its antennae, gap columns apart.
func Beside(lines []string, gap int) []string {
	face := Bot()
	out := append([]string{face[0]}, lines...)
	for i := 1; i < len(face); i++ {
		if i >= len(out) {
			out = append(out, "")
		}
		out[i] = face[i] + strings.Repeat(" ", gap) + out[i]
	}
	return out
}

// Center centers the block of lines in width columns.
func Center(lines []string, width int) []string {
	w := 0
	for _, l := range lines {
		w = max(w, ansi.StringWidth(l))
	}
	pad := strings.Repeat(" ", max(width-w, 0)/2)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Truncate(pad+l, width, "…")
	}
	return out
}

// Pad is the columns between a card's border and its content.
const Pad = 2

// Card is a rounded box whose border takes one color of ink per column.
type Card struct {
	ink   []color.Color
	lines []string
}

// NewCard starts a card width columns wide; its content is Inner(width)
// wide.
func NewCard(width int) *Card {
	k := &Card{ink: lipgloss.Blend1D(width, sink(theme.Accent), sink(theme.Info), sink(theme.Agent))}
	k.lines = []string{k.edge("╭", "╮", "", "")}
	return k
}

// Inner is the content width of a card width columns wide.
func Inner(width int) int { return width - 2 - 2*Pad }

// Lines is how many lines the card has so far.
func (k *Card) Lines() int { return len(k.lines) }

func (k *Card) Add(lines ...string) {
	w := len(k.ink)
	pad := strings.Repeat(" ", Pad)
	for _, l := range lines {
		l = ansi.Truncate(l, Inner(w), "…")
		fill := strings.Repeat(" ", max(Inner(w)-ansi.StringWidth(l), 0))
		k.lines = append(k.lines, k.stroke("│", 0)+pad+l+fill+pad+k.stroke("│", w-1))
	}
}

// Section draws a divider titled title, styled by the caller, with a note
// at its right end.
func (k *Card) Section(title, note string) {
	k.lines = append(k.lines, k.edge("├", "┤", title, note))
}

func (k *Card) Close() []string {
	return append(k.lines, k.edge("╰", "╯", "", ""))
}

func (k *Card) edge(l, r, title, note string) string {
	w := len(k.ink)
	if title == "" || w-2 < ansi.StringWidth(title)+4 {
		return k.stroke(l+strings.Repeat("─", w-2)+r, 0)
	}
	head := k.stroke(l+"─ ", 0) + title
	x := 3 + ansi.StringWidth(title)
	if fill := w - x - ansi.StringWidth(note) - 5; note != "" && fill >= 2 {
		return head + k.stroke(" "+strings.Repeat("─", fill)+" ", x) +
			theme.SubtleText.Render(note) + k.stroke(" ─"+r, w-3)
	}
	return head + k.stroke(" "+strings.Repeat("─", w-x-2)+r, x)
}

func (k *Card) stroke(s string, x int) string {
	var b strings.Builder
	for i, r := range []rune(s) {
		b.WriteString(lipgloss.NewStyle().Foreground(k.ink[x+i]).Render(string(r)))
	}
	return b.String()
}

func sink(c color.Color) color.Color {
	return lipgloss.Blend1D(3, c, theme.Faint)[1]
}
