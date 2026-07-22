package tui

// Welcome banner + shimmer scan effect used by status line.

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
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

// wordmarkGlyphs defines 3-row compact block letters for the welcome logo.
// Each glyph is 5 cols wide × 3 rows tall, built from half-block chars
// (▀▄█) so row-by-row color gradients read as top-lit → mid → bottom-shaded.
var wordmarkGlyphs = map[rune][3]string{
	'C': {"▄████", "█    ", "▀████"},
	'O': {"▄███▄", "█   █", "▀███▀"},
	'D': {"████▄", "█   █", "████▀"},
	'E': {"█████", "████ ", "█████"},
	'B': {"████▄", "███▀▄", "████▀"},
	'T': {"█████", "  █  ", "  █  "},
}

// renderWordmark composes "CODEBOT" as a 3-row 3D wordmark:
//
//	row 1 — top highlight (Strong, max contrast — pure black/white per theme)
//	row 2 — mid body      (Brand color, solid)
//	row 3 — bottom shade  (BrandSoft, simulates ambient shadow)
//
// Total glyph width: 7 letters × 5 cols + 6 single-col gaps = 41 cols.
func renderWordmark() string {
	const word = "CODEBOT"

	var r1, r2, r3 strings.Builder
	for i, c := range word {
		if i > 0 {
			r1.WriteByte(' ')
			r2.WriteByte(' ')
			r3.WriteByte(' ')
		}
		g := wordmarkGlyphs[c]
		r1.WriteString(g[0])
		r2.WriteString(g[1])
		r3.WriteString(g[2])
	}

	topStyle := lipgloss.NewStyle().Foreground(Strong)
	midStyle := lipgloss.NewStyle().Foreground(Brand).Bold(true)
	botStyle := lipgloss.NewStyle().Foreground(BrandSoft)

	return strings.Join([]string{
		topStyle.Render(r1.String()),
		midStyle.Render(r2.String()),
		botStyle.Render(r3.String()),
	}, "\n")
}

const wordmarkWidth = 41 // see renderWordmark — must match the glyph layout

// frameCard wraps pre-rendered body lines in a rounded BrandSoft frame with an
// embedded title tag, the card chrome shared by the welcome banner and the
// onboarding wizard. Body lines are padded to width-4 inner columns; a line
// wider than that is truncated (ANSI-aware) so the frame can never break.
func frameCard(titleTag string, body []string, width int) string {
	bc := lipgloss.NewStyle().Foreground(BrandSoft)
	innerW := width - 4
	topDash := max(width-lipgloss.Width(titleTag)-5, 1)
	framed := make([]string, 0, len(body)+2)
	framed = append(framed, bc.Render("╭─ ")+titleTag+" "+bc.Render(strings.Repeat("─", topDash)+"╮"))
	for _, line := range body {
		if lipgloss.Width(line) > innerW {
			line = truncate.StringWithTail(line, uint(innerW), "…")
		}
		pad := max(innerW-lipgloss.Width(line), 0)
		framed = append(framed, bc.Render("│ ")+line+strings.Repeat(" ", pad)+bc.Render(" │"))
	}
	framed = append(framed, bc.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return strings.Join(framed, "\n")
}

// pairClamp clamps two chips to jointly fit one line of the given width
// (separator included): if both fit they pass through; otherwise the shorter
// chip keeps its natural size (guaranteed at least half the line) and the
// longer one is middle-clamped into the remainder.
func pairClamp(a, b string, width int) (string, string) {
	const sepW = 3
	if a == "" || b == "" {
		return middleClamp(a, width), middleClamp(b, width)
	}
	avail := width - sepW
	wa, wb := len([]rune(a)), len([]rune(b))
	if wa+wb <= avail {
		return a, b
	}
	if wa <= wb {
		a = middleClamp(a, max(avail/2, avail-wb))
		return a, middleClamp(b, avail-len([]rune(a)))
	}
	b = middleClamp(b, max(avail/2, avail-wa))
	return middleClamp(a, avail-len([]rune(b))), b
}

// middleClamp truncates s to at most w runes with a middle ellipsis, keeping
// both ends visible — for paths and model ids the head and tail carry the
// meaning, the middle is expendable.
func middleClamp(s string, w int) string {
	r := []rune(s)
	if len(r) <= w || w <= 1 {
		return s
	}
	head := (w - 1) / 2
	tail := w - 1 - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

func (m *Model) renderWelcome() string {
	// Sized to hug the widest content (wordmark 41 + tagline 44 + subline 48)
	// plus light breathing padding. Capped tighter than before so the card
	// feels like a focused splash rather than a stretched banner.
	width := 64
	if m.Width > 0 {
		width = min(max(m.Width-4, 52), 68)
	}

	ver := m.Version
	if ver == "" {
		ver = "dev"
	}

	bc := lipgloss.NewStyle().Foreground(BrandSoft)
	innerW := width - 4

	// Center a line of measured visible width within innerW.
	center := func(visibleW int, line string) string {
		pad := max((innerW-visibleW)/2, 0)
		return strings.Repeat(" ", pad) + line
	}

	// --- logo ---
	var logoLines []string
	if innerW >= wordmarkWidth {
		for _, line := range strings.Split(renderWordmark(), "\n") {
			logoLines = append(logoLines, center(wordmarkWidth, line))
		}
	} else {
		// Narrow fallback: stylised inline wordmark.
		fallback := WelcomeTitleStyle.Render("CODEBOT")
		logoLines = append(logoLines, center(lipgloss.Width(fallback), fallback))
	}

	// --- tagline block ---
	tagline := WelcomeTitleStyle.Render("Long-running coding agent for the terminal.")
	subline := WelcomeBodyStyle.Render("Small execution kernel. Strong runtime harness.")
	ruleLen := min(wordmarkWidth, innerW-4)
	rule := bc.Render(strings.Repeat("─", ruleLen))
	hint := WelcomeMutedStyle.Render("/  commands     ⏎  send     esc  abort")

	taglineLines := []string{
		center(lipgloss.Width(tagline), tagline),
		center(lipgloss.Width(subline), subline),
		center(ruleLen, rule),
		center(lipgloss.Width(hint), hint),
	}

	// --- footer: two fixed lines — provider · model, then path · branch ---
	// pairClamp budgets each line jointly (the short chip keeps its natural
	// size, the long one takes the rest), so a deep path or long model id
	// shrinks with a middle ellipsis instead of pushing the border out.
	sep := ContextChipStyle.Render(" · ")
	var footerLines []string
	appendPair := func(a, b string, styleA, styleB lipgloss.Style) {
		a, b = pairClamp(a, b, innerW)
		var parts []string
		if a != "" {
			parts = append(parts, styleA.Render(a))
		}
		if b != "" {
			parts = append(parts, styleB.Render(b))
		}
		if len(parts) > 0 {
			footerLines = append(footerLines, strings.Join(parts, sep))
		}
	}
	var modelChip, pathChip, branchChip string
	if m.ModelName != "" {
		modelChip = m.formatModelChip()
		// Thinking level rides on the model chip — same muted style, and
		// pairClamp keeps the tail visible when the model id runs long.
		if m.ReasoningEffort != "" && m.ReasoningEffort != "off" {
			modelChip += " · thinking " + m.ReasoningEffort
		}
	}
	if m.Cwd != "" {
		pathChip = ShortenPath(m.Cwd)
	}
	if m.GitBranch != "" {
		branchChip = "branch " + m.GitBranch
	}
	appendPair(m.Provider, modelChip, ContextChipAccentStyle, ContextChipStyle)
	appendPair(pathChip, branchChip, ContextChipStyle, ContextChipStyle)

	// --- assemble ---
	var body []string
	body = append(body, "")
	body = append(body, logoLines...)
	body = append(body, "")
	body = append(body, taglineLines...)
	if len(footerLines) > 0 {
		body = append(body, "")
		for _, line := range footerLines {
			body = append(body, center(lipgloss.Width(line), line))
		}
	}
	body = append(body, "")

	titleTag := WelcomeKickerStyle.Render("codebot") + " " + ContextChipAccentStyle.Render(ver)
	result := "\n" + frameCard(titleTag, body, width)
	if m.MCPLoading {
		result += "\n" + InputHintStyle.Render("  ") + m.ToolSpinner.View() + InputHintStyle.Render(" MCP servers connecting...")
	}
	return result
}
