package tui

import "github.com/charmbracelet/lipgloss"

// Color palette — terminal-friendly, restrained, and readable.
var (
	ColorPrimary     = lipgloss.Color("#3FA796") // teal, core brand/action
	ColorPrimarySoft = lipgloss.Color("#2F6F68") // muted teal for borders/labels
	ColorAccent      = lipgloss.Color("#D89B5B") // warm amber accent
	ColorUser        = lipgloss.Color("#8AB4F8") // calm blue
	ColorAssistant   = lipgloss.Color("#B8E1DD") // pale teal
	ColorTool        = lipgloss.Color("#E5B567") // amber/yellow
	ColorError       = lipgloss.Color("#E06C75") // soft red
	ColorSuccess     = lipgloss.Color("#98C379") // green
	ColorMuted       = lipgloss.Color("243")     // medium gray
	ColorThinking    = lipgloss.Color("240")     // dim gray, secondary text
	ColorToken       = lipgloss.Color("249")     // light gray
	ColorCommand     = lipgloss.Color("#78C6E7") // cool cyan
	ColorRunning     = lipgloss.Color("#5FD7FF") // dedicated live-status cyan
	ColorStatusBg    = lipgloss.Color("236")     // dark neutral background
	ColorSeparator   = lipgloss.Color("241")     // neutral separator
	ColorBorder      = lipgloss.Color("245")
	ColorShell       = lipgloss.Color("#D16D9E") // shell hint
	ColorPanelBg     = lipgloss.Color("235")
	ColorPanelEdge   = lipgloss.Color("239")
	ColorSubtleBg    = lipgloss.Color("237")
	ColorTitle       = lipgloss.Color("#F0E6D2")
	ColorSoftText    = lipgloss.Color("252")
)

// Tool blocks
var (
	ToolIconStyle = lipgloss.NewStyle().Foreground(ColorTool)

	ToolNameStyle = lipgloss.NewStyle().Foreground(ColorTool).Bold(true)

	ToolArgsStyle = lipgloss.NewStyle().Foreground(ColorMuted)

	ToolResultStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))

	ToolPathStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#82AAFF"))
)

// Thinking body
var ThinkingBodyStyle = lipgloss.NewStyle().
	Foreground(ColorThinking).
	Italic(true)

// Assistant icon
var AssistantIconStyle = lipgloss.NewStyle().
	Foreground(ColorAssistant).
	Bold(true)

// Error
var ErrorStyle = lipgloss.NewStyle().
	Foreground(ColorError).
	Bold(true)

// Command / system output
var CommandStyle = lipgloss.NewStyle().
	Foreground(ColorCommand)

// Short system notifications (e.g. "Switched to model", "Session cleared")
var SystemMsgStyle = lipgloss.NewStyle().
	Foreground(ColorMuted).
	Italic(true)

// Image tag selected (reverse video highlight)
var ImageSelectedStyle = lipgloss.NewStyle().Reverse(true)

// Separator
var SeparatorStyle = lipgloss.NewStyle().
	Foreground(ColorSeparator)

// Shell mode separator
var ShellSeparatorStyle = lipgloss.NewStyle().
	Foreground(ColorShell)

// Welcome banner
var (
	WelcomeTitleStyle = lipgloss.NewStyle().Foreground(ColorTitle).Bold(true)

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
				BorderForeground(ColorPrimarySoft).
				Padding(0, 1)

	CommandPaletteTitleStyle = lipgloss.NewStyle().
					Foreground(ColorTitle).
					Bold(true)

	CommandPaletteSectionStyle = lipgloss.NewStyle().
					Foreground(ColorPrimarySoft).
					Bold(true)

	CommandPaletteSelectedStyle = lipgloss.NewStyle().
					Foreground(ColorPrimary).
					Bold(true)

	CommandPaletteItemStyle = lipgloss.NewStyle().
				Foreground(ColorSoftText)

	CommandPaletteDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("249"))

	CommandPaletteSelectedDescStyle = lipgloss.NewStyle().
					Foreground(ColorAssistant)

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
	BorderForeground(ColorTool).
	Padding(0, 1)

// Plan mode tag
var PlanTagStyle = lipgloss.NewStyle().Foreground(ColorPrimary)

// Assistant markdown container
var AssistantMarkdownBlockStyle = lipgloss.NewStyle().
	BorderLeft(true).
	BorderForeground(ColorPrimarySoft).
	Padding(0, 1).
	MarginLeft(1)

// General purpose
var (
	MutedStyle = lipgloss.NewStyle().Foreground(ColorMuted)

	BoxBorderStyle = lipgloss.NewStyle().Foreground(ColorBorder)

	QueuedMsgStyle = lipgloss.NewStyle().Foreground(ColorMuted).Italic(true)

	TokenStyle = lipgloss.NewStyle().Foreground(ColorToken)

	DiffAddStyle = lipgloss.NewStyle().Foreground(ColorSuccess)

	DiffRemoveStyle = lipgloss.NewStyle().Foreground(ColorError)

	DiffInverseAddStyle = lipgloss.NewStyle().Foreground(ColorSuccess).Reverse(true)

	DiffInverseRemoveStyle = lipgloss.NewStyle().Foreground(ColorError).Reverse(true)

	ReplyLabelStyle = lipgloss.NewStyle().Foreground(ColorAssistant)
)

var (
	CardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPanelEdge).
			Padding(0, 1)

	CardTitleStyle = lipgloss.NewStyle().
			Foreground(ColorTitle).
			Bold(true)

	CardSectionStyle = lipgloss.NewStyle().
				Foreground(ColorPrimarySoft).
				Bold(true)

	InputPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPrimarySoft).
			Padding(0, 1)

	InputHintStyle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	ContextChipStyle = lipgloss.NewStyle().
				Foreground(ColorSoftText)

	ContextChipAccentStyle = lipgloss.NewStyle().
				Foreground(ColorPrimary).
				Bold(true)

	ContextChipWarnStyle = lipgloss.NewStyle().
				Foreground(ColorAccent).
				Bold(true)

	WelcomeFrameStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorPrimarySoft).
				Padding(0, 1)

	WelcomeKickerStyle = lipgloss.NewStyle().
				Foreground(ColorPrimary).
				Bold(true)

	WelcomeBodyStyle = lipgloss.NewStyle().
				Foreground(ColorSoftText)

	WelcomeMutedStyle = lipgloss.NewStyle().
				Foreground(ColorMuted)

	TagStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true)

	TagSubtleStyle = lipgloss.NewStyle().
			Foreground(ColorSoftText)

	ImageTagStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary)

	TaskCardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPanelEdge).
			Padding(0, 1)

	TaskProgressStyle = lipgloss.NewStyle().
				Foreground(ColorTitle)

	PermissionTitleStyle = lipgloss.NewStyle().
				Foreground(ColorAccent).
				Bold(true)

	AskCardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPrimarySoft).
			Padding(0, 1)
)

func CommandPaletteKindBadge(kind string) string {
	label := "[" + kind + "]"
	style := lipgloss.NewStyle().Foreground(ColorMuted)

	switch kind {
	case "builtin":
		style = style.Foreground(ColorCommand)
	case "custom":
		style = style.Foreground(ColorTool)
	case "skill":
		style = style.Foreground(ColorSuccess)
	}
	return style.Render(label)
}

func CommandPaletteCategoryBadge(category string) string {
	label := "[" + category + "]"
	style := lipgloss.NewStyle().Foreground(ColorMuted)

	switch category {
	case "session", "config":
		style = style.Foreground(ColorTool)
	case "plan":
		style = style.Foreground(ColorAccent)
	case "exit":
		style = style.Foreground(ColorError)
	default:
		style = style.Foreground(ColorMuted)
	}
	return style.Render(label)
}

func CommandPaletteIdleBadge(needsIdle bool) string {
	if !needsIdle {
		return ""
	}
	return lipgloss.NewStyle().
		Foreground(ColorMuted).
		Render("[idle]")
}
