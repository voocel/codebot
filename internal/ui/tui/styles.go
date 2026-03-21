package tui

import (
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// Color palette — dark terminal optimized.
var (
	ColorAccent      = lipgloss.Color("#7C6FE0") // soft purple, secondary accent
	ColorPrimary     = lipgloss.Color("#4EC9B0") // primary brand color
	ColorPrimarySoft = lipgloss.Color("108")     // muted primary used for borders/section labels
	ColorUser        = lipgloss.Color("#5FAFFF") // bright blue
	ColorAssistant   = lipgloss.Color("#C792EA") // soft purple/magenta
	ColorTool        = lipgloss.Color("#FFCB6B") // amber/yellow
	ColorError       = lipgloss.Color("#FF5370") // soft red
	ColorSuccess     = lipgloss.Color("#C3E88D") // green
	ColorMuted       = lipgloss.Color("243")     // medium gray
	ColorThinking    = lipgloss.Color("240")     // dim gray, distinctly muted vs assistant text
	ColorToken       = lipgloss.Color("249")     // light gray
	ColorCommand     = lipgloss.Color("#89DDFF") // cyan
	ColorStatusBg    = lipgloss.Color("236")     // dark background
	ColorSeparator   = lipgloss.Color("243")     // medium gray
	ColorBorder      = lipgloss.Color("246")
)

// Tool blocks
var (
	ToolIconStyle = lipgloss.NewStyle().Foreground(ColorTool)

	ToolNameStyle = lipgloss.NewStyle().Foreground(ColorTool).Bold(true)

	ToolArgsStyle = lipgloss.NewStyle().Foreground(ColorMuted)

	ToolResultStyle = lipgloss.NewStyle().Foreground(ColorMuted)
)

// Thinking body
var ThinkingBodyStyle = lipgloss.NewStyle().
	Foreground(ColorThinking).
	Italic(true)

// Assistant icon
var AssistantIconStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("252"))

// Error
var ErrorStyle = lipgloss.NewStyle().
	Foreground(ColorError).
	Bold(true)

// Command / system output
var CommandStyle = lipgloss.NewStyle().
	Foreground(ColorCommand)

// Image tag selected (reverse video highlight)
var ImageSelectedStyle = lipgloss.NewStyle().Reverse(true)

// Separator
var SeparatorStyle = lipgloss.NewStyle().
	Foreground(ColorSeparator)

// Welcome banner
var (
	WelcomeTitleStyle = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

	WelcomeDetailStyle = lipgloss.NewStyle().Foreground(ColorMuted)
)

// Footer
var FooterStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("235")).
	Background(lipgloss.Color("#b5e6b5")).
	Padding(0, 1)

// Selection menu (plan approval, etc.)
var (
	ChoiceActiveStyle   = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	ChoiceInactiveStyle = lipgloss.NewStyle().Foreground(ColorMuted)
)

// Command palette
var (
	CommandPaletteStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorPrimarySoft)

	CommandPaletteTitleStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("250")).
					Bold(true)

	CommandPaletteSectionStyle = lipgloss.NewStyle().
					Foreground(ColorPrimarySoft).
					Bold(true)

	CommandPaletteSelectedStyle = lipgloss.NewStyle().
					Foreground(ColorPrimary).
					Bold(true)

	CommandPaletteItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))

	CommandPaletteDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("249"))

	CommandPaletteSelectedDescStyle = lipgloss.NewStyle().
					Foreground(ColorPrimary)

	CommandPaletteHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("246"))
)

// Plan box
var PlanBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(ColorAccent).
	Padding(0, 1)

// Subagent result card
var SubagentCardStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(ColorTool). // amber, matches tool header
	Padding(0, 1)

// Plan mode tag
var PlanTagStyle = lipgloss.NewStyle().Foreground(ColorAccent)

// General purpose
var (
	MutedStyle = lipgloss.NewStyle().Foreground(ColorMuted)

	BoxBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	QueuedMsgStyle = lipgloss.NewStyle().Foreground(ColorMuted).Italic(true)

	TokenStyle = lipgloss.NewStyle().Foreground(ColorToken)

	DiffAddStyle = lipgloss.NewStyle().Foreground(ColorSuccess)

	DiffRemoveStyle = lipgloss.NewStyle().Foreground(ColorError)

	DiffInverseAddStyle = lipgloss.NewStyle().Foreground(ColorSuccess).Reverse(true)

	DiffInverseRemoveStyle = lipgloss.NewStyle().Foreground(ColorError).Reverse(true)

	ReplyLabelStyle = lipgloss.NewStyle().Foreground(ColorAssistant)
)

func CommandPaletteKindBadge(kind string) string {
	label := kind
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("232")).
		Background(ColorSeparator).
		Padding(0, 1)

	switch kind {
	case "builtin":
		style = style.Background(ColorCommand)
	case "custom":
		style = style.Background(ColorTool)
	case "skill":
		style = style.Background(ColorSuccess)
	}
	return style.Render(label)
}

func CommandPaletteCategoryBadge(category string) string {
	label := category
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("232")).
		Background(ColorSeparator).
		Padding(0, 1)

	switch category {
	case "session", "config":
		style = style.Background(ColorTool)
	case "plan":
		style = style.Background(ColorAccent)
	case "exit":
		style = style.Background(ColorError)
	default:
		style = style.Background(ColorSeparator).Foreground(lipgloss.Color("255"))
	}
	return style.Render(label)
}

func CommandPaletteIdleBadge(needsIdle bool) string {
	if !needsIdle {
		return ""
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("255")).
		Background(lipgloss.Color("240")).
		Padding(0, 1).
		Render("idle")
}

// NewGlamourRenderer creates a glamour markdown renderer with the given width.
func NewGlamourRenderer(width int) *glamour.TermRenderer {
	r, _ := glamour.NewTermRenderer(
		// Avoid terminal probing escape sequences leaking into input on some terminals.
		glamour.WithStandardStyle("notty"),
		glamour.WithWordWrap(width),
	)
	return r
}
