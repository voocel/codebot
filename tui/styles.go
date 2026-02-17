package tui

import (
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// Colors
var (
	ColorPrimary   = lipgloss.Color("63")
	ColorUser      = lipgloss.Color("39")
	ColorAssistant = lipgloss.Color("213")
	ColorTool      = lipgloss.Color("214")
	ColorError     = lipgloss.Color("196")
	ColorMuted     = lipgloss.Color("241")
	ColorThinking  = lipgloss.Color("103")
	ColorCommand   = lipgloss.Color("77")
	ColorToken     = lipgloss.Color("249")
)

// Styles
var (
	StatusBarStyle = lipgloss.NewStyle().
			Background(ColorPrimary).
			Foreground(lipgloss.Color("230")).
			Padding(0, 1).
			Bold(true)

	UserPrefixStyle = lipgloss.NewStyle().
			Foreground(ColorUser).
			Bold(true)

	AssistantPrefixStyle = lipgloss.NewStyle().
				Foreground(ColorAssistant).
				Bold(true)

	ToolNameStyle = lipgloss.NewStyle().
			Foreground(ColorTool).
			Bold(true)

	ToolResultStyle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	ErrorStyle = lipgloss.NewStyle().
			Foreground(ColorError).
			Bold(true)

	StreamingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	MutedStyle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	ThinkingStyle = lipgloss.NewStyle().
			Foreground(ColorThinking).
			Italic(true)

	CommandStyle = lipgloss.NewStyle().
			Foreground(ColorCommand)

	TokenStyle = lipgloss.NewStyle().
			Foreground(ColorToken)

	FooterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)
)

// NewGlamourRenderer creates a glamour markdown renderer with the given width.
func NewGlamourRenderer(width int) *glamour.TermRenderer {
	r, _ := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	return r
}
