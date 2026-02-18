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
	ColorSeparator = lipgloss.Color("237")     // subtle separator
)

// Status bar
var StatusBarStyle = lipgloss.NewStyle().
	Background(ColorStatusBg).
	Foreground(lipgloss.Color("250")).
	Padding(0, 1)

// User prompt prefix (inline, no label)
var UserPromptStyle = lipgloss.NewStyle().
	Foreground(ColorUser).
	Bold(true)

// Assistant label
var AssistantLabelStyle = lipgloss.NewStyle().
	Foreground(ColorAssistant).
	Bold(true)

// Tool blocks
var (
	ToolIconStyle = lipgloss.NewStyle().
			Foreground(ColorTool)

	ToolNameStyle = lipgloss.NewStyle().
			Foreground(ColorTool).
			Bold(true)

	ToolArgsStyle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	ToolResultStyle = lipgloss.NewStyle().
			Foreground(ColorMuted)
)

// Thinking blocks
var (
	ThinkingLabelStyle = lipgloss.NewStyle().
				Foreground(ColorThinking).
				Italic(true)

	ThinkingBodyStyle = lipgloss.NewStyle().
				Foreground(ColorThinking).
				Italic(true)
)

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
	WelcomeTitleStyle = lipgloss.NewStyle().
				Foreground(ColorAccent).
				Bold(true)

	WelcomeDetailStyle = lipgloss.NewStyle().
				Foreground(ColorMuted)
)

// Footer
var FooterStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("245")).
	Background(ColorStatusBg).
	Padding(0, 1)

// General purpose
var (
	MutedStyle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	TokenStyle = lipgloss.NewStyle().
			Foreground(ColorToken)

	DiffAddStyle = lipgloss.NewStyle().
			Foreground(ColorSuccess)

	DiffRemoveStyle = lipgloss.NewStyle().
			Foreground(ColorError)
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
