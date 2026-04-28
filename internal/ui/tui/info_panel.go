package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	reflowwrap "github.com/muesli/reflow/wrap"
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
	width int // 0 = no wrap; otherwise the panel's available render width
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

// SetWidth tells the panel how many columns it has to render in. When set,
// values that exceed the value column wrap to subsequent lines indented under
// the value column. width <= 0 disables wrapping (legacy behavior).
func (p *InfoPanel) SetWidth(width int) {
	p.width = width
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

	labelStyle := lipgloss.NewStyle().Foreground(Muted)
	valueStyle := lipgloss.NewStyle().Foreground(Text)
	hintStyle := lipgloss.NewStyle().Foreground(Muted)
	warnStyle := lipgloss.NewStyle().Foreground(Accent)
	sectionStyle := CardSectionStyle
	titleStyle := CardTitleStyle

	const leftPad = 2 // gutter before the label column
	// valueBudget is the width available for the value column. When width is
	// unset or so narrow that wrapping would produce slivers, fall back to
	// no-wrap (single-line) rendering.
	valueBudget := 0
	if p.width > 0 {
		valueBudget = p.width - leftPad - colWidth
		if valueBudget < 12 {
			valueBudget = 0
		}
	}
	indent := strings.Repeat(" ", leftPad+colWidth)

	var sb strings.Builder
	if p.title != "" {
		sb.WriteString(titleStyle.Render(p.title))
		sb.WriteString("\n")
	}

	writeRow := func(label, value string, vs lipgloss.Style) {
		// Fast path: no wrap configured or value already fits.
		if valueBudget == 0 || lipgloss.Width(value) <= valueBudget {
			sb.WriteString(labelStyle.Render(fmt.Sprintf("  %-*s", colWidth, label)))
			sb.WriteString(vs.Render(value))
			sb.WriteString("\n")
			return
		}
		// reflow/wrap force-breaks at displayed width — handles paths and
		// other word-less strings correctly, and preserves ANSI color spans.
		wrapped := strings.Split(
			strings.TrimRight(reflowwrap.String(value, valueBudget), "\n"), "\n")
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
		case rowWarn:
			writeRow(r.label, r.value, warnStyle)
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}
