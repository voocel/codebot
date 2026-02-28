package tui

import (
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// Color palette — dark terminal optimized.
var (
	ColorAccent    = lipgloss.Color("#7C6FE0") // soft purple, primary accent
	ColorUser      = lipgloss.Color("#5FAFFF") // bright blue
	ColorAssistant = lipgloss.Color("#C792EA") // soft purple/magenta
	ColorTool      = lipgloss.Color("#FFCB6B") // amber/yellow
	ColorError     = lipgloss.Color("#FF5370") // soft red
	ColorSuccess   = lipgloss.Color("#C3E88D") // green
	ColorMuted     = lipgloss.Color("243")     // medium gray
	ColorThinking  = lipgloss.Color("240")     // dim gray, distinctly muted vs assistant text
	ColorToken     = lipgloss.Color("249")     // light gray
	ColorCommand   = lipgloss.Color("#89DDFF") // cyan
	ColorStatusBg  = lipgloss.Color("236")     // dark background
	ColorSeparator = lipgloss.Color("249")     // light gray, match welcome detail
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

	TokenStyle = lipgloss.NewStyle().Foreground(ColorToken)

	DiffAddStyle = lipgloss.NewStyle().Foreground(ColorSuccess)

	DiffRemoveStyle = lipgloss.NewStyle().Foreground(ColorError)

	DiffInverseAddStyle = lipgloss.NewStyle().Foreground(ColorSuccess).Reverse(true)

	DiffInverseRemoveStyle = lipgloss.NewStyle().Foreground(ColorError).Reverse(true)
)

// NewGlamourRenderer creates a glamour markdown renderer with the given width.
func NewGlamourRenderer(width int) *glamour.TermRenderer {
	r, _ := glamour.NewTermRenderer(
		// Avoid terminal probing escape sequences leaking into input on some terminals.
		glamour.WithStandardStyle("notty"),
		glamour.WithWordWrap(width),
	)
	return r
}
