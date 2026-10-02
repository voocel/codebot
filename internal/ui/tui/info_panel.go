package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// InfoPanel lays out labelled values in two columns, for /status, /settings
// and /context.
//
//	p := NewInfoPanel(width)
//	p.Row("Model", "claude-sonnet-4-6")
//	p.Section("Runtime")
//	p.Hint("Config", "~/.codebot/settings.json")
//	p.Render()
type InfoPanel struct {
	rows  []panelRow
	width int
}

type panelRowKind int

const (
	rowNormal  panelRowKind = iota
	rowHint                 // subdued value (paths, metadata)
	rowSection              // section divider
	rowBlank                // empty line
)

type panelRow struct {
	kind  panelRowKind
	label string
	value string
}

// NewInfoPanel creates a panel width columns wide; a value too wide for its
// column wraps under it.
func NewInfoPanel(width int) *InfoPanel {
	return &InfoPanel{width: width}
}

// Row adds a normal key-value row.
func (p *InfoPanel) Row(label, value string) {
	p.rows = append(p.rows, panelRow{kind: rowNormal, label: label, value: value})
}

// Hint adds a subdued metadata row.
func (p *InfoPanel) Hint(label, value string) {
	p.rows = append(p.rows, panelRow{kind: rowHint, label: label, value: value})
}

// Section adds a section header with a blank line before it.
func (p *InfoPanel) Section(title string) {
	p.rows = append(p.rows, panelRow{kind: rowBlank})
	p.rows = append(p.rows, panelRow{kind: rowSection, label: title})
}

// Render produces the final styled string.
func (p *InfoPanel) Render() string {
	if len(p.rows) == 0 {
		return ""
	}

	// Compute label column width from content.
	maxLabel := 0
	for _, r := range p.rows {
		if r.kind == rowNormal || r.kind == rowHint {
			if n := len(r.label); n > maxLabel {
				maxLabel = n
			}
		}
	}
	colWidth := maxLabel + 2 // padding

	labelStyle := lipgloss.NewStyle().Foreground(Muted)
	valueStyle := lipgloss.NewStyle().Foreground(Text)
	hintStyle := lipgloss.NewStyle().Foreground(Muted)
	sectionStyle := CardSectionStyle

	const leftPad = 2 // gutter before the label column
	// valueBudget is the width of the value column; one too narrow to wrap
	// into is not wrapped at all.
	valueBudget := p.width - leftPad - colWidth
	if valueBudget < 12 {
		valueBudget = 0
	}
	indent := strings.Repeat(" ", leftPad+colWidth)

	var sb strings.Builder

	writeRow := func(label, value string, vs lipgloss.Style) {
		if valueBudget == 0 || lipgloss.Width(value) <= valueBudget {
			sb.WriteString(labelStyle.Render(fmt.Sprintf("  %-*s", colWidth, label)))
			sb.WriteString(vs.Render(value))
			sb.WriteString("\n")
			return
		}
		wrapped := strings.Split(wrapTextWidth(value, valueBudget), "\n")
		for i, line := range wrapped {
			if i == 0 {
				sb.WriteString(labelStyle.Render(fmt.Sprintf("  %-*s", colWidth, label)))
			} else {
				sb.WriteString(indent)
			}
			sb.WriteString(vs.Render(line))
			sb.WriteString("\n")
		}
	}

	for _, r := range p.rows {
		switch r.kind {
		case rowBlank:
			sb.WriteString("\n")
		case rowSection:
			sb.WriteString(sectionStyle.Render(r.label))
			sb.WriteString("\n")
		case rowNormal:
			writeRow(r.label, r.value, valueStyle)
		case rowHint:
			writeRow(r.label, r.value, hintStyle)
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}
