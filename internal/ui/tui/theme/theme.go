// Package theme holds the TUI's palette and styles. Colors name roles, not
// hues; Init must pick the hues before anything renders.
package theme

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

var Dark bool

var (
	Fg     color.Color // body text
	Strong color.Color // highest contrast: the assistant's bullet, titles
	Muted  color.Color // secondary text
	Subtle color.Color // hints, thinking
	Faint  color.Color // rules, connectors

	Surface   color.Color // the user's prompt band
	Selection color.Color // selected text

	Accent  color.Color // the brand: prompt, focus
	Live    color.Color // the status line's spinner
	Warm    color.Color // running work, emphasis
	Success color.Color
	Danger  color.Color
	Info    color.Color // links, inline code
	Path    color.Color
	Shell   color.Color // "!" commands
	Agent   color.Color // sub-agents

	DiffAdd, DiffRemove         color.Color // line backgrounds
	DiffAddWord, DiffRemoveWord color.Color // changed words within a line

	// Glint ramps dim to bright for the status line's shimmer.
	Glint [16]color.Color
)

var (
	Text       lipgloss.Style
	Bold       lipgloss.Style
	MutedText  lipgloss.Style
	SubtleText lipgloss.Style
	FaintText  lipgloss.Style
	AccentText lipgloss.Style
	WarmText   lipgloss.Style
	ErrorText  lipgloss.Style
	OKText     lipgloss.Style
	PathText   lipgloss.Style
	Selected   lipgloss.Style // the highlighted row of a list
	Key        lipgloss.Style // a key in a hint, "esc"
)

func init() { Init(true) }

// Detect lets CODEBOT_THEME=light|dark override the terminal's reported
// background.
func Detect() {
	switch strings.ToLower(os.Getenv("CODEBOT_THEME")) {
	case "light":
		Init(false)
	case "dark":
		Init(true)
	default:
		Init(lipgloss.HasDarkBackground(os.Stdin, os.Stdout))
	}
}

func Init(dark bool) {
	Dark = dark
	c := func(light, dark string) color.Color {
		return lipgloss.LightDark(Dark)(lipgloss.Color(light), lipgloss.Color(dark))
	}

	Fg = c("#24292F", "#E4E4E7")
	Strong = c("#000000", "#FFFFFF")
	Muted = c("#57606A", "#A1A1AA")
	Subtle = c("#8C959F", "#71717A")
	Faint = c("#C4CBD3", "#45454D")

	Surface = c("#F2F4F7", "#232328")
	Selection = c("#C8E1FF", "#3A4A63")

	Accent = c("#0E7C6B", "#5CCFB4")
	Live = c("31", "153")
	Warm = c("#9A6700", "#E8B86D")
	Success = c("#1A7F37", "#6CCB8A")
	Danger = c("#CF222E", "#F07178")
	Info = c("#0969DA", "#82AAFF")
	Path = c("#0550AE", "#8AB4F8")
	Shell = c("#A04870", "#E37FB1")
	Agent = c("#8250DF", "#C3A6F7")

	DiffAdd = c("#E6FFEC", "#16301F")
	DiffRemove = c("#FFEBE9", "#3C1A1E")
	DiffAddWord = c("#ABF2BC", "#24603A")
	DiffRemoveWord = c("#FFC1BC", "#76272F")

	glintLight := [...]string{"243", "243", "244", "244", "245", "245", "246", "246", "247", "247", "248", "248", "249", "250", "251", "252"}
	glintDark := [...]string{"246", "246", "247", "248", "248", "249", "250", "250", "251", "252", "252", "253", "254", "255", "255", "255"}
	for i := range Glint {
		Glint[i] = c(glintLight[i], glintDark[i])
	}

	s := lipgloss.NewStyle
	Text = s().Foreground(Fg)
	Bold = s().Bold(true)
	MutedText = s().Foreground(Muted)
	SubtleText = s().Foreground(Subtle)
	FaintText = s().Foreground(Faint)
	AccentText = s().Foreground(Accent)
	WarmText = s().Foreground(Warm)
	ErrorText = s().Foreground(Danger)
	OKText = s().Foreground(Success)
	PathText = s().Foreground(Path)
	Selected = s().Foreground(Accent).Bold(true)
	Key = s().Foreground(Muted).Bold(true)
}

// Hint renders "key action" pairs, like "↑↓ select · esc close".
func Hint(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteString(SubtleText.Render(" · "))
		}
		b.WriteString(Key.Render(pairs[i]))
		b.WriteString(SubtleText.Render(" " + pairs[i+1]))
	}
	return b.String()
}
