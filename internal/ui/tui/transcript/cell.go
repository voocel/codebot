// Package transcript turns a conversation into cells that render at any
// width. Cells built from live events and from a saved history must look the
// same, so a conversation renders identically live, restored and in a
// sub-agent's view.
package transcript

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/ui/tui/theme"
)

type Params struct {
	Width int
	// Expanded shows thinking and tool output in full.
	Expanded bool
	// Live cells render as a function of Now, however often they render.
	Now time.Time
}

// A Cell keeps its content, not its rendering, so it can render at any
// width.
type Cell interface {
	// Render returns lines no wider than p.Width.
	Render(p Params) []string
	// Version changes whenever Render may return something else for the
	// same Params.
	Version() uint64
	// Live cells animate with p.Now, so their rendering must not be cached.
	Live() bool
}

type Toggler interface {
	Toggle()
}

// An Attached cell renders with no gap below the cell above it.
type Attached interface {
	Attached() bool
}

type rev struct{ v uint64 }

func (r *rev) Version() uint64 { return r.v }
func (r *rev) bump()           { r.v++ }

type still struct{}

func (still) Live() bool { return false }

const (
	bullet    = "●"
	connector = "⎿"
)

func indent(lines []string, head string) []string {
	pad := strings.Repeat(" ", ansi.StringWidth(head))
	out := make([]string, len(lines))
	for i, l := range lines {
		if i == 0 {
			out[i] = head + l
		} else {
			out[i] = pad + l
		}
	}
	return out
}

func body(lines []string, width int) []string {
	for i, l := range lines {
		lines[i] = fit(l, bodyWidth(width))
	}
	return indent(lines, "  "+theme.FaintText.Render(connector)+" ")
}

func bodyWidth(width int) int { return max(width-4, 8) }

// clip keeps the first and last few lines, noting how many it omitted.
func clip(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return lines
	}
	head := (max - 1) / 2
	tail := max - 1 - head
	out := append([]string{}, lines[:head]...)
	out = append(out, more(len(lines)-head-tail))
	return append(out, lines[len(lines)-tail:]...)
}

func tail(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return lines
	}
	return append([]string{more(len(lines) - max + 1)}, lines[len(lines)-max+1:]...)
}

func more(n int) string {
	return theme.SubtleText.Render("… +"+strconv.Itoa(n)+" lines") + theme.FaintText.Render(" (ctrl+o to expand)")
}

func fit(s string, width int) string { return ansi.Truncate(s, width, "…") }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return s
}
