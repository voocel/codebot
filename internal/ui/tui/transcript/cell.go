// Package transcript turns a conversation into cells that render at any
// width: a prompt, a reply, a tool call, a notice. A Transcript builds them
// the same way from the events of a live run and from a saved history, so a
// conversation looks the same live, restored and in a sub-agent's view.
package transcript

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// Params says how cells render.
type Params struct {
	Width int
	// Expanded shows thinking and tool output in full.
	Expanded bool
	// Now animates live cells: what they show is a function of the time,
	// however often they render.
	Now time.Time
}

// A Cell is one block of the conversation. It keeps what it shows, not how it
// looked, so it renders anew at any width.
type Cell interface {
	// Render returns the cell's lines, none wider than p.Width.
	Render(p Params) []string
	// Version changes whenever Render may return something else for the
	// same Params.
	Version() uint64
	// Live reports whether the cell animates, rendering with p.Now, so that
	// what it renders must not be kept.
	Live() bool
}

// A Toggler is a cell the user can expand or collapse on its own.
type Toggler interface {
	Toggle()
}

// Attached is a cell that reads as part of the one above it, with no gap
// between them.
type Attached interface {
	Attached() bool
}

// rev implements Version.
type rev struct{ v uint64 }

func (r *rev) Version() uint64 { return r.v }
func (r *rev) bump()           { r.v++ }

// still implements Live for cells that do not animate.
type still struct{}

func (still) Live() bool { return false }

const (
	bullet    = "●"
	connector = "⎿"
)

// indent puts head before the first line and as many spaces as it is wide
// before the rest.
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

// body hangs lines under a header in a cell width wide: "  ⎿ " before the
// first, spaces before the rest.
func body(lines []string, width int) []string {
	for i, l := range lines {
		lines[i] = fit(l, bodyWidth(width))
	}
	return indent(lines, "  "+theme.FaintText.Render(connector)+" ")
}

// bodyWidth is the width left to a body under a header.
func bodyWidth(width int) int { return max(width-4, 8) }

// clip shortens lines to about max, keeping the first and last few, with a
// note of how many it left out between them.
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

// tail keeps the last max lines, with a note of how many came before.
func tail(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return lines
	}
	return append([]string{more(len(lines) - max + 1)}, lines[len(lines)-max+1:]...)
}

func more(n int) string {
	return theme.SubtleText.Render("… +"+strconv.Itoa(n)+" lines") + theme.FaintText.Render(" (ctrl+o to expand)")
}

// fit truncates s to width.
func fit(s string, width int) string { return ansi.Truncate(s, width, "…") }

// firstLine is the first line of s, marked "…" when there are more.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return s
}
