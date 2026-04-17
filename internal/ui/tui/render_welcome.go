package tui

// Welcome banner + shimmer scan effect used by status line.

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	scanStepsLight = [...]string{"243", "243", "244", "244", "245", "245", "246", "246", "247", "247", "248", "248", "249", "250", "251", "252"}
	scanStepsDark  = [...]string{"246", "246", "247", "248", "248", "249", "250", "250", "251", "252", "252", "253", "254", "255", "255", "255"}
)

// scanText renders text with a scanning light effect.
// speed: how fast the light moves (chars per second).
// band:  number of fully bright characters at center.
// slope: number of gradient characters on each side of the band.
func scanText(text string, now float64, speed float64, band, slope int) string {
	runes := []rune(text)
	n := len(runes)
	if n == 0 {
		return ""
	}

	total := float64(n + band + slope*2)
	center := math.Mod(now*speed, total)

	var b strings.Builder
	for i, r := range runes {
		dist := math.Abs(float64(i) - center)
		halfBand := float64(band) / 2.0

		var brightness float64
		if dist <= halfBand {
			brightness = 1.0
		} else if slope > 0 {
			fade := (dist - halfBand) / float64(slope)
			if fade < 1.0 {
				brightness = 1.0 - fade
			}
		}

		idx := int(brightness * 15)
		b.WriteString(lipgloss.NewStyle().Foreground(scanStepColor(idx)).Render(string(r)))
	}
	return b.String()
}

func scanStepColor(idx int) lipgloss.TerminalColor {
	if idx < 0 {
		idx = 0
	}
	if idx > 15 {
		idx = 15
	}
	if lipgloss.HasDarkBackground() {
		return lipgloss.Color(scanStepsDark[idx])
	}
	return lipgloss.Color(scanStepsLight[idx])
}

func (m *Model) renderWelcome() string {
	width := 84
	if m.Width > 0 {
		width = min(max(m.Width-4, 56), 96)
	}

	ver := m.Version
	if ver == "" {
		ver = "dev"
	}

	bc := lipgloss.NewStyle().Foreground(ColorPrimarySoft)
	edge := WelcomeKickerStyle
	title := WelcomeTitleStyle

	// Terminal window logo with drop shadow
	dotR := lipgloss.NewStyle().Foreground(ColorError)
	dotY := lipgloss.NewStyle().Foreground(ColorTool)
	dotG := lipgloss.NewStyle().Foreground(ColorSuccess)
	cursor := lipgloss.NewStyle().Foreground(ColorAccent)
	shadow := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "249", Dark: "238"})

	logoLines := []string{
		edge.Render("╭──────────────────────╮"),
		edge.Render("│") + " " + dotR.Render("●") + " " + dotY.Render("●") + " " + dotG.Render("●") + "    " + title.Render("CODEBOT") + "     " + edge.Render("│"),
		edge.Render("├──────────────────────┤") + shadow.Render("░"),
		edge.Render("│") + "                      " + edge.Render("│") + shadow.Render("░"),
		edge.Render("│") + "       " + edge.Render("❯_") + " " + cursor.Render("█") + "           " + edge.Render("│") + shadow.Render("░"),
		edge.Render("│") + "                      " + edge.Render("│") + shadow.Render("░"),
		edge.Render("╰──────────────────────╯") + shadow.Render("░"),
		" " + shadow.Render(strings.Repeat("░", 23)),
	}
	logo := lipgloss.NewStyle().Width(27).Render(strings.Join(logoLines, "\n"))

	// Vertical separator (match logo height)
	const logoHeight = 8
	sepParts := make([]string, logoHeight)
	for i := range sepParts {
		sepParts[i] = bc.Render("│")
	}
	sep := strings.Join(sepParts, "\n")

	// Right panel
	rightWidth := max(width-34, 20)
	rightLines := []string{
		WelcomeTitleStyle.Render("Long-running coding agent for the terminal."),
		"",
		WelcomeBodyStyle.Render("Small execution kernel. Strong runtime harness."),
		bc.Render(strings.Repeat("─", min(rightWidth, 44))),
		WelcomeMutedStyle.Render("/ commands · Enter send · Esc abort"),
	}
	right := lipgloss.NewStyle().Width(rightWidth).Render(strings.Join(rightLines, "\n"))

	body := lipgloss.JoinHorizontal(lipgloss.Top, logo, " ", sep, " ", right)

	// Footer: provider · model · path · branch (version in top border)
	var footerBits []string
	if m.Provider != "" {
		footerBits = append(footerBits, ContextChipAccentStyle.Render(m.Provider))
	}
	if m.ModelName != "" {
		footerBits = append(footerBits, ContextChipStyle.Render(m.formatModelChip()))
	}
	if m.Cwd != "" {
		footerBits = append(footerBits, ContextChipStyle.Render(shortenPath(m.Cwd)))
	}
	if m.GitBranch != "" {
		footerBits = append(footerBits, ContextChipStyle.Render("branch "+m.GitBranch))
	}
	footer := strings.Join(footerBits, ContextChipStyle.Render(" · "))

	// Assemble inner content
	content := strings.Join([]string{"", body, "", footer}, "\n")

	// Manual frame with title embedded in top border
	innerW := width - 4
	titleTag := WelcomeKickerStyle.Render("Codebot") + " " + ContextChipAccentStyle.Render(ver)
	titleW := lipgloss.Width(titleTag)
	topDash := max(width-titleW-5, 1)
	topLine := bc.Render("╭─ ") + titleTag + " " + bc.Render(strings.Repeat("─", topDash)+"╮")
	botLine := bc.Render("╰" + strings.Repeat("─", width-2) + "╯")

	lines := strings.Split(content, "\n")
	framed := make([]string, 0, len(lines)+2)
	framed = append(framed, topLine)
	for _, line := range lines {
		pad := max(innerW-lipgloss.Width(line), 0)
		framed = append(framed, bc.Render("│ ")+line+strings.Repeat(" ", pad)+bc.Render(" │"))
	}
	framed = append(framed, botLine)

	result := "\n" + strings.Join(framed, "\n")
	if m.EnvHint != "" {
		result += "\n" + InputHintStyle.Render("  "+m.EnvHint)
	}
	if m.MCPLoading {
		result += "\n" + InputHintStyle.Render("  ") + m.ToolSpinner.View() + InputHintStyle.Render(" MCP servers connecting...")
	}
	return result
}
