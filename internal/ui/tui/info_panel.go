package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// InfoPanel builds structured info displays used by /settings, /context, etc.
//
//	p := NewInfoPanel("Settings")
//	p.Row("Provider", "anthropic")
//	p.Row("Model", "claude-sonnet-4-6")
//	p.Section("Runtime")
//	p.Row("Thinking", "low")
//	p.Hint("Config", "~/.codebot/settings.json")
//	fmt.Print(p.Render())
type InfoPanel struct {
	title string
	rows  []panelRow
}

type panelRowKind int

const (
	rowNormal panelRowKind = iota
	rowHint                // subdued value (paths, metadata)
	rowWarn                // accent-colored value
	rowSection             // section divider
	rowBlank               // empty line
)

type panelRow struct {
	kind  panelRowKind
	label string
	value string
}

// NewInfoPanel creates a panel with the given title.
func NewInfoPanel(title string) *InfoPanel {
	return &InfoPanel{title: title}
}

// Row adds a normal key-value row.
func (p *InfoPanel) Row(label, value string) {
	p.rows = append(p.rows, panelRow{kind: rowNormal, label: label, value: value})
}

// Hint adds a subdued metadata row.
func (p *InfoPanel) Hint(label, value string) {
	p.rows = append(p.rows, panelRow{kind: rowHint, label: label, value: value})
}

// Warn adds an accent-colored row.
func (p *InfoPanel) Warn(label, value string) {
	p.rows = append(p.rows, panelRow{kind: rowWarn, label: label, value: value})
}

// Section adds a section header with a blank line before it.
func (p *InfoPanel) Section(title string) {
	p.rows = append(p.rows, panelRow{kind: rowBlank})
	p.rows = append(p.rows, panelRow{kind: rowSection, label: title})
}

// Blank adds an empty line.
func (p *InfoPanel) Blank() {
	p.rows = append(p.rows, panelRow{kind: rowBlank})
}

// Render produces the final styled string.
func (p *InfoPanel) Render() string {
	if len(p.rows) == 0 {
		return ""
	}

	// Compute label column width from content.
	maxLabel := 0
	for _, r := range p.rows {
		if r.kind == rowNormal || r.kind == rowHint || r.kind == rowWarn {
			if n := len(r.label); n > maxLabel {
				maxLabel = n
			}
		}
	}
	colWidth := maxLabel + 2 // padding

	labelStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	valueStyle := lipgloss.NewStyle().Foreground(ColorSoftText)
	hintStyle := lipgloss.NewStyle().Foreground(ColorToken)
	warnStyle := lipgloss.NewStyle().Foreground(ColorAccent)
	sectionStyle := CardSectionStyle
	titleStyle := CardTitleStyle

	var sb strings.Builder
	if p.title != "" {
		sb.WriteString(titleStyle.Render(p.title))
		sb.WriteString("\n")
	}

	for _, r := range p.rows {
		switch r.kind {
		case rowBlank:
			sb.WriteString("\n")
		case rowSection:
			sb.WriteString(sectionStyle.Render(r.label))
			sb.WriteString("\n")
		case rowNormal:
			sb.WriteString(labelStyle.Render(fmt.Sprintf("  %-*s", colWidth, r.label)))
			sb.WriteString(valueStyle.Render(r.value))
			sb.WriteString("\n")
		case rowHint:
			sb.WriteString(labelStyle.Render(fmt.Sprintf("  %-*s", colWidth, r.label)))
			sb.WriteString(hintStyle.Render(r.value))
			sb.WriteString("\n")
		case rowWarn:
			sb.WriteString(labelStyle.Render(fmt.Sprintf("  %-*s", colWidth, r.label)))
			sb.WriteString(warnStyle.Render(r.value))
			sb.WriteString("\n")
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}
