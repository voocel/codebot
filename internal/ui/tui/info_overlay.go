package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// InfoOverlayTab describes one pane inside an InfoOverlayFrame.
// Body is invoked on each render so it can reflect live session state.
type InfoOverlayTab struct {
	Name string
	Body func() string
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
		body := strings.TrimRight(f.Tabs[f.Active].Body(), "\n")
		if body != "" {
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

func renderInfoOverlayHeader(f InfoOverlayFrame) string {
	titleStyle := lipgloss.NewStyle().Foreground(ColorTitle).Bold(true)
	subtitleStyle := MutedStyle
	activeTabStyle := lipgloss.NewStyle().
		Foreground(ColorTitle).
		Background(ColorPanelEdge).
		Bold(true).
		Padding(0, 1)
	inactiveTabStyle := lipgloss.NewStyle().
		Foreground(ColorMuted).
		Padding(0, 1)

	var parts []string
	parts = append(parts, "  "+titleStyle.Render(f.Title))
	if f.Subtitle != "" {
		parts = append(parts, subtitleStyle.Render(f.Subtitle))
	}

	for i, t := range f.Tabs {
		if i == f.Active {
			parts = append(parts, activeTabStyle.Render(t.Name))
		} else {
			parts = append(parts, inactiveTabStyle.Render(t.Name))
		}
	}

	return strings.Join(parts, " ")
}
