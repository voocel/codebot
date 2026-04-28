package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// InfoOverlayTab describes one pane inside an InfoOverlayFrame.
// Body is invoked on each render so it can reflect live session state. The
// width passed in is the inner content width (already subtracted from the
// frame chrome) — body renderers should hand it to InfoPanel.SetWidth or
// equivalent so long values wrap instead of overflowing the terminal.
type InfoOverlayTab struct {
	Name string
	Body func(width int) string
}

// InfoOverlayFrame renders a modal-style info overlay: title + tab bar +
// divider + body + footer hint. Used by information commands that want to
// replace the input area rather than scroll past it.
//
//	frame := tui.InfoOverlayFrame{
//	    Title: "Settings",
//	    Tabs:  []tui.InfoOverlayTab{{Name: "general", Body: renderGeneral}},
//	    Hint:  "Esc to close",
//	    Width: width,
//	}
//	fmt.Print(frame.Render())
type InfoOverlayFrame struct {
	Title    string
	Subtitle string
	Tabs     []InfoOverlayTab
	Active   int
	Hint     string
	Width    int
	// Height is the terminal viewport height. When > 0, every tab body is
	// normalised to the same number of rows: long bodies are clipped so the
	// header stays on screen, short bodies are padded so switching tabs does
	// not change the overlay's total height (which would scroll the header
	// up/down in inline render mode).
	Height int
}

// Render produces the full styled overlay body.
func (f InfoOverlayFrame) Render() string {
	width := f.Width
	if width <= 0 {
		width = 80
	}
	inner := max(width-4, 20)

	var sb strings.Builder

	sb.WriteString(renderInfoOverlayHeader(f))
	sb.WriteString("\n")
	sb.WriteString("  " + SeparatorStyle.Render(strings.Repeat("─", inner)))
	sb.WriteString("\n")

	if len(f.Tabs) > 0 && f.Active >= 0 && f.Active < len(f.Tabs) {
		body := strings.TrimRight(f.Tabs[f.Active].Body(inner), "\n")
		if body != "" {
			body = fitBody(body, f.bodyBudget())
			sb.WriteString("\n")
			sb.WriteString(body)
			sb.WriteString("\n")
		}
	}

	if f.Hint != "" {
		sb.WriteString("\n")
		sb.WriteString(MutedStyle.Italic(true).Render("  " + f.Hint))
	}

	return sb.String()
}

// bodyBudget returns the maximum number of body lines that fit without
// pushing the header out of view. Returns 0 (= unlimited) when Height is
// unknown. The constants below mirror the surrounding scaffolding rendered
// in Render(): header(1) + separator(1) + leading blank(1) + trailing
// blank(1) + hint blank+line(2) = 6 reserved rows.
func (f InfoOverlayFrame) bodyBudget() int {
	if f.Height <= 0 {
		return 0
	}
	const reserved = 6
	return max(f.Height-reserved, 3)
}

// fitBody normalises body to exactly targetLines rows. Long bodies are
// clipped with a muted "(N more lines...)" tail; short bodies are padded
// with blank lines. The constant height keeps the overlay's total render
// height stable across tab switches so the header doesn't shift vertically
// in inline (non-altscreen) mode. targetLines <= 0 disables both effects.
func fitBody(body string, targetLines int) string {
	if targetLines <= 0 {
		return body
	}
	lines := strings.Split(body, "\n")
	switch {
	case len(lines) > targetLines:
		hidden := len(lines) - (targetLines - 1)
		kept := append([]string(nil), lines[:targetLines-1]...)
		kept = append(kept, MutedStyle.Italic(true).Render(
			fmt.Sprintf("  ... %d more lines (resize terminal to see all)", hidden)))
		return strings.Join(kept, "\n")
	case len(lines) < targetLines:
		pad := make([]string, targetLines-len(lines))
		return strings.Join(append(lines, pad...), "\n")
	default:
		return body
	}
}

func renderInfoOverlayHeader(f InfoOverlayFrame) string {
	titleStyle := lipgloss.NewStyle().Foreground(Brand).Bold(true)
	subtitleStyle := MutedStyle
	// Filled pill on the active tab — Brand teal background with a Strong
	// (white-on-dark / black-on-light) foreground gives strong contrast that
	// the previous Border-on-Title combo lacked.
	activeTabStyle := lipgloss.NewStyle().
		Foreground(Strong).
		Background(Brand).
		Bold(true).
		Padding(0, 1)
	inactiveTabStyle := lipgloss.NewStyle().
		Foreground(Muted).
		Padding(0, 1)
	dividerStyle := lipgloss.NewStyle().Foreground(Subtle)

	var parts []string
	parts = append(parts, "  "+titleStyle.Render(f.Title))
	if f.Subtitle != "" {
		parts = append(parts, subtitleStyle.Render(f.Subtitle))
	}

	if len(f.Tabs) > 0 {
		parts = append(parts, dividerStyle.Render("│"))
		for i, t := range f.Tabs {
			if i == f.Active {
				parts = append(parts, activeTabStyle.Render(t.Name))
			} else {
				parts = append(parts, inactiveTabStyle.Render(t.Name))
			}
		}
	}

	return strings.Join(parts, " ")
}
