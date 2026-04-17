package tui

import "github.com/charmbracelet/lipgloss"

// Color palette — terminal-friendly, restrained, and readable.
var (
	ColorPrimary   = lipgloss.Color("#3FA796")                            // teal, core brand/action
	ColorAccent    = lipgloss.Color("#D89B5B")                            // warm amber accent
	ColorUser      = lipgloss.AdaptiveColor{Light: "31", Dark: "#9CC2F9"} // softer user blue, still distinct from assistant/tool colors
	ColorAssistant = lipgloss.Color("#B8E1DD")                            // pale teal
	ColorTool      = lipgloss.Color("#E5B567")                            // amber/yellow
	ColorToolDim   = lipgloss.AdaptiveColor{Light: "94", Dark: "137"}     // dimmed amber — same hue, lower weight (for tool args)
	ColorToolInk   = lipgloss.Color("#1C1C1C")                            // near-black for text on amber chip
	ColorError     = lipgloss.Color("#E06C75")                            // soft red
	ColorSuccess   = lipgloss.Color("#98C379")                            // green
	ColorCommand   = lipgloss.Color("#78C6E7")                            // cool cyan
	ColorShell     = lipgloss.Color("#D16D9E")                            // shell hint

	// Claude Code 也是先走语义色，再让组件消费语义：
	// text / inactive / subtle / promptBorder / suggestion。
	// 这里保持同样的思路，只保留一条中性色阶，避免每块 UI 自己挑灰度。
	ColorText       = lipgloss.AdaptiveColor{Light: "236", Dark: "252"} // 默认正文
	ColorTextMuted  = lipgloss.AdaptiveColor{Light: "242", Dark: "247"} // 次级信息 / 状态
	ColorTextSubtle = lipgloss.AdaptiveColor{Light: "246", Dark: "243"} // placeholder / thinking / 弱提示
	ColorChrome     = lipgloss.AdaptiveColor{Light: "248", Dark: "242"} // 分隔线 / 边框 / 输入框 chrome

	ColorPrimarySoft = lipgloss.AdaptiveColor{Light: "30", Dark: "72"} // muted teal for borders/labels
	ColorMuted       = ColorTextMuted
	ColorThinking    = ColorTextSubtle
	ColorToken       = lipgloss.AdaptiveColor{Light: "244", Dark: "245"} // neutral metadata
	ColorRunning     = lipgloss.AdaptiveColor{Light: "31", Dark: "153"}  // live-status spinner / strong live chrome
	ColorStatusBg    = lipgloss.AdaptiveColor{Light: "254", Dark: "236"} // soft strip behind user echoes
	ColorSeparator   = ColorChrome
	ColorBorder      = lipgloss.AdaptiveColor{Light: "247", Dark: "241"}
	ColorPanelEdge   = lipgloss.AdaptiveColor{Light: "248", Dark: "241"}
	ColorTitle       = lipgloss.AdaptiveColor{Light: "235", Dark: "255"}
	ColorSoftText    = ColorText
	ColorInputChrome = lipgloss.AdaptiveColor{Light: "245", Dark: "244"}
	ColorPlaceholder = ColorTextSubtle
	ColorPath        = lipgloss.AdaptiveColor{Light: "26", Dark: "111"} // path / file highlight
	ColorToolMeta    = lipgloss.AdaptiveColor{Light: "243", Dark: "246"} // tool line numbers / tails
)

// ---------------------------------------------------------------------------
// Shared frame builders
// ---------------------------------------------------------------------------
// card is the common "rounded border + padding" envelope used by every
// panel / card / dialog surface. Only the border color varies across callers.
func card(borderColor lipgloss.TerminalColor) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)
}

// inputPanel is the top+bottom-rule frame used by the input textarea.
// Only the border color varies (chrome vs shell accent).
func inputPanel(borderColor lipgloss.TerminalColor) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.Border{Top: "─", Bottom: "─"}).
		BorderTop(true).
		BorderBottom(true).
		BorderLeft(false).
		BorderRight(false).
		BorderForeground(borderColor).
		Padding(0, 1)
}

// Tool blocks
var (
	ToolIconStyle = lipgloss.NewStyle().Foreground(ColorTool)

	// Tool name chip — amber background with near-black ink, à la Claude Code.
	// No padding: keeps the chip flush with trailing args like "Bash(cmd)".
	ToolNameStyle = lipgloss.NewStyle().
			Background(ColorTool).
			Foreground(ColorToolInk).
			Bold(true)

	// Tool args — dimmed amber, same hue as the name for visual cohesion.
	ToolArgsStyle = lipgloss.NewStyle().Foreground(ColorToolDim)

	// Tool result body — normal text brightness, content is the focus.
	ToolResultStyle = lipgloss.NewStyle().Foreground(ColorText)

	ToolPathStyle = lipgloss.NewStyle().Foreground(ColorPath)
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

// Welcome banner
var WelcomeTitleStyle = lipgloss.NewStyle().Foreground(ColorTitle).Bold(true)

// Selection highlight for plan approval and similar pickers.
var ChoiceActiveStyle = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

// Command palette
var (
	CommandPaletteStyle = card(ColorPrimarySoft)

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
				Foreground(ColorToken)

	CommandPaletteSelectedDescStyle = lipgloss.NewStyle().
					Foreground(ColorAssistant)

	CommandPaletteHintStyle = lipgloss.NewStyle().
				Foreground(ColorBorder)
)

// Plan box
var PlanBoxStyle = card(ColorAccent)

// Subagent result card
var SubagentCardStyle = card(ColorTool)

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
	CardTitleStyle = lipgloss.NewStyle().
			Foreground(ColorTitle).
			Bold(true)

	CardSectionStyle = lipgloss.NewStyle().
				Foreground(ColorPrimarySoft).
				Bold(true)

	InputPanelStyle      = inputPanel(ColorInputChrome)
	ShellInputPanelStyle = inputPanel(ColorShell)

	// ShellAccentStyle is used for both the prompt caret ("❯") and the "!" prefix
	// when the input is in shell mode — they share the same foreground/weight by design.
	ShellAccentStyle = lipgloss.NewStyle().
				Foreground(ColorShell).
				Bold(true)

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

	WelcomeKickerStyle = lipgloss.NewStyle().
				Foreground(ColorPrimary).
				Bold(true)

	WelcomeBodyStyle = lipgloss.NewStyle().
				Foreground(ColorSoftText)

	WelcomeMutedStyle = lipgloss.NewStyle().
				Foreground(ColorMuted)

	TagSubtleStyle = lipgloss.NewStyle().
			Foreground(ColorSoftText)

	ImageTagStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary)

	TaskCardStyle = card(ColorPanelEdge)

	TaskProgressStyle = lipgloss.NewStyle().
				Foreground(ColorTitle)

	PermissionTitleStyle = lipgloss.NewStyle().
				Foreground(ColorAccent).
				Bold(true)

	AskCardStyle = card(ColorPrimarySoft)
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
