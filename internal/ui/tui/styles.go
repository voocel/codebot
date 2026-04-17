package tui

// styles.go — component styles. All colors come from theme.go tokens.
// Keep styles free of raw hex/ANSI values; change the theme, not the component.

import "github.com/charmbracelet/lipgloss"

// ---------------------------------------------------------------------------
// Frame builders
// ---------------------------------------------------------------------------

// card is the common "rounded border + padding" envelope used by panels,
// dialogs, and cards. Only the border color varies across callers.
func card(borderColor lipgloss.TerminalColor) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)
}

// inputPanel wraps the textarea with top+bottom horizontal rules — mirroring
// Claude Code's visual (thin ─ separators hugging the prompt, no side borders
// so the input flows edge-to-edge). Left/right are omitted to keep the caret
// anchored flush-left without a surrounding box.
func inputPanel(borderColor lipgloss.TerminalColor) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, true, false).
		BorderForeground(borderColor).
		Padding(0, 1, 0, 0)
}

// ---------------------------------------------------------------------------
// Tool blocks
// ---------------------------------------------------------------------------

var (
	// Bullet — green for successful tool call, red for failure, à la Claude Code.
	ToolIconStyle  = lipgloss.NewStyle().Foreground(Success)
	ErrorIconStyle = lipgloss.NewStyle().Foreground(Danger)

	// Tool name — bold only, default foreground. Lets terminal theme show through.
	ToolNameStyle = lipgloss.NewStyle().Bold(true)

	// Tool args and result body intentionally carry no color — they inherit the
	// terminal's default foreground so the user's theme stays in charge.
	ToolArgsStyle   = lipgloss.NewStyle()
	ToolResultStyle = lipgloss.NewStyle()

	ToolPathStyle = lipgloss.NewStyle().Foreground(Path)
)

// ---------------------------------------------------------------------------
// Messages / thinking / roles
// ---------------------------------------------------------------------------

var (
	// Assistant bullet — pure white on dark / pure black on light, bold.
	// Uses Strong rather than the terminal default so contrast against the
	// Subtle-gray thinking bullet is guaranteed under any terminal palette.
	AssistantIconStyle = lipgloss.NewStyle().Foreground(Strong).Bold(true)

	// Thinking bullet — dim gray, same family as the italic thinking body.
	ThinkingIconStyle = lipgloss.NewStyle().Foreground(Subtle)
	ThinkingBodyStyle = lipgloss.NewStyle().Foreground(Subtle).Italic(true)

	ReplyLabelStyle = lipgloss.NewStyle().Foreground(RoleAssistant)

	SystemMsgStyle = lipgloss.NewStyle().Foreground(Muted).Italic(true)

	QueuedMsgStyle = lipgloss.NewStyle().Foreground(Muted).Italic(true)
)

// ---------------------------------------------------------------------------
// Status / feedback
// ---------------------------------------------------------------------------

var (
	ErrorStyle   = lipgloss.NewStyle().Foreground(Danger).Bold(true)
	CommandStyle = lipgloss.NewStyle().Foreground(Info)
	MutedStyle   = lipgloss.NewStyle().Foreground(Muted)
	TokenStyle   = lipgloss.NewStyle().Foreground(Muted)
)

// ---------------------------------------------------------------------------
// Diff (edit result)
// ---------------------------------------------------------------------------

var (
	DiffAddStyle           = lipgloss.NewStyle().Foreground(Success)
	DiffRemoveStyle        = lipgloss.NewStyle().Foreground(Danger)
	DiffInverseAddStyle    = lipgloss.NewStyle().Foreground(Success).Reverse(true)
	DiffInverseRemoveStyle = lipgloss.NewStyle().Foreground(Danger).Reverse(true)
)

// ---------------------------------------------------------------------------
// Chrome / separators
// ---------------------------------------------------------------------------

var (
	SeparatorStyle = lipgloss.NewStyle().Foreground(Separator)
	BoxBorderStyle = lipgloss.NewStyle().Foreground(Border)

	CardTitleStyle   = lipgloss.NewStyle().Foreground(Title).Bold(true)
	CardSectionStyle = lipgloss.NewStyle().Foreground(BrandSoft).Bold(true)

	// ConnectorStyle dims the tree connector "⎿  " so it recedes visually.
	ConnectorStyle = lipgloss.NewStyle().Foreground(Subtle)
)

// TreeConnector is the Claude-Code-style result connector: U+23BF (⎿) plus
// two spaces for wide, legible alignment under the tool header.
const TreeConnector = "⎿  "

// ConnectorPad matches TreeConnector's width (3 cells) for continuation lines.
const ConnectorPad = "   "

// ---------------------------------------------------------------------------
// Input area
// ---------------------------------------------------------------------------

var (
	InputPanelStyle      = inputPanel(InputRule)
	ShellInputPanelStyle = inputPanel(RoleShell)

	// ShellAccentStyle colors both the prompt caret "❯" and the "!" prefix
	// when the input is in shell mode — they share the same style by design.
	ShellAccentStyle = lipgloss.NewStyle().Foreground(RoleShell).Bold(true)

	InputHintStyle = lipgloss.NewStyle().Foreground(Muted)

	ImageSelectedStyle = lipgloss.NewStyle().Reverse(true)
	ImageTagStyle      = lipgloss.NewStyle().Foreground(Brand)
)

// ---------------------------------------------------------------------------
// Welcome banner
// ---------------------------------------------------------------------------

var (
	WelcomeTitleStyle  = lipgloss.NewStyle().Foreground(Title).Bold(true)
	WelcomeKickerStyle = lipgloss.NewStyle().Foreground(Brand).Bold(true)
	WelcomeBodyStyle   = lipgloss.NewStyle().Foreground(Text)
	WelcomeMutedStyle  = lipgloss.NewStyle().Foreground(Muted)
)

// ---------------------------------------------------------------------------
// Command palette
// ---------------------------------------------------------------------------

var (
	CommandPaletteStyle = card(BrandSoft)

	CommandPaletteTitleStyle        = lipgloss.NewStyle().Foreground(Title).Bold(true)
	CommandPaletteSectionStyle      = lipgloss.NewStyle().Foreground(BrandSoft).Bold(true)
	CommandPaletteSelectedStyle     = lipgloss.NewStyle().Foreground(Brand).Bold(true)
	CommandPaletteItemStyle         = lipgloss.NewStyle().Foreground(Text)
	CommandPaletteDescStyle         = lipgloss.NewStyle().Foreground(Muted)
	CommandPaletteSelectedDescStyle = lipgloss.NewStyle().Foreground(RoleAssistant)
	CommandPaletteHintStyle         = lipgloss.NewStyle().Foreground(Border)
)

// ---------------------------------------------------------------------------
// Context bar / plan / permission / tasks
// ---------------------------------------------------------------------------

var (
	ContextChipStyle       = lipgloss.NewStyle().Foreground(Muted)
	ContextChipAccentStyle = lipgloss.NewStyle().Foreground(Brand)
	ContextChipPathStyle   = lipgloss.NewStyle().Foreground(Path)
	// Transient hints like "Press Ctrl+C again to exit" or "bash mode" — muted
	// gray, not a loud warning. Claude Code keeps these understated.
	ContextChipWarnStyle = lipgloss.NewStyle().Foreground(Muted)

	ChoiceActiveStyle = lipgloss.NewStyle().Foreground(Accent).Bold(true)

	PlanBoxStyle      = card(Accent)
	SubagentCardStyle = card(Accent)

	TaskCardStyle     = card(Border)
	TaskProgressStyle = lipgloss.NewStyle().Foreground(Title)

	PermissionTitleStyle = lipgloss.NewStyle().Foreground(Accent).Bold(true)
	AskCardStyle         = card(BrandSoft)

	TagSubtleStyle = lipgloss.NewStyle().Foreground(Text)
)

// ---------------------------------------------------------------------------
// Palette badges (functions because color depends on kind/category)
// ---------------------------------------------------------------------------

func CommandPaletteKindBadge(kind string) string {
	label := "[" + kind + "]"
	style := lipgloss.NewStyle().Foreground(Muted)
	switch kind {
	case "builtin":
		style = style.Foreground(Info)
	case "custom":
		style = style.Foreground(Accent)
	case "skill":
		style = style.Foreground(Success)
	}
	return style.Render(label)
}

func CommandPaletteCategoryBadge(category string) string {
	label := "[" + category + "]"
	style := lipgloss.NewStyle().Foreground(Muted)
	switch category {
	case "session", "config":
		style = style.Foreground(Accent)
	case "plan":
		style = style.Foreground(Accent)
	case "exit":
		style = style.Foreground(Danger)
	}
	return style.Render(label)
}

func CommandPaletteIdleBadge(needsIdle bool) string {
	if !needsIdle {
		return ""
	}
	return lipgloss.NewStyle().Foreground(Muted).Render("[idle]")
}
