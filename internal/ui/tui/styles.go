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

// inputPanel wraps the textarea with top+bottom horizontal rules: thin ─
// separators hugging the prompt, no side borders so the input flows edge-to-
// edge and the caret stays flush-left.
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
	// Bullet — green for successful tool call, red for failure.
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

// The whole row — gutter + body — sits on the same colored band so the
// diff reads as a single visual unit. Gutter additionally carries a fg so
// the line number / sigil stand out; body has bg only, leaving the existing
// foreground (syntax highlighting, path tokens) untouched.
var (
	DiffAddGutterStyle    = lipgloss.NewStyle().Foreground(Success).Background(DiffAddBg)
	DiffRemoveGutterStyle = lipgloss.NewStyle().Foreground(Danger).Background(DiffRemoveBg)

	DiffAddBodyStyle    = lipgloss.NewStyle().Background(DiffAddBg)
	DiffRemoveBodyStyle = lipgloss.NewStyle().Background(DiffRemoveBg)

	// Word-level intra-line emphasis: deeper bg shade in the same hue, no fg
	// override. The body's foreground (or future syntax highlighting) flows
	// through; only the background tells the eye "this part actually changed".
	DiffAddInverseStyle    = lipgloss.NewStyle().Background(DiffAddBgStrong)
	DiffRemoveInverseStyle = lipgloss.NewStyle().Background(DiffRemoveBgStrong)

	// /diff's --stat bar: plain foreground sigils (no background fill, unlike
	// the edit-result rows above) for the per-file +/- change graph.
	DiffStatAddStyle    = lipgloss.NewStyle().Foreground(Success)
	DiffStatRemoveStyle = lipgloss.NewStyle().Foreground(Danger)
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

// TreeConnector is the result connector: U+23BF (⎿) plus two spaces for
// alignment under the tool header.
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
	CommandPaletteSelectedStyle     = lipgloss.NewStyle().Foreground(Brand).Bold(true)
	CommandPaletteItemStyle         = lipgloss.NewStyle().Foreground(Text)
	CommandPaletteDescStyle         = lipgloss.NewStyle().Foreground(Muted)
	CommandPaletteSelectedDescStyle = lipgloss.NewStyle().Foreground(RoleAssistant)
	CommandPaletteHintStyle         = lipgloss.NewStyle().Foreground(Border)
	// Trailing Kind tag rendered on the right of each row (skill / custom).
	// Uses the Meta token (one step below desc's Muted) so the tag recedes
	// to "dim metadata" weight — desc carries the meaning, tag just labels.
	CommandPaletteTagStyle = lipgloss.NewStyle().Foreground(Meta)
)

// ---------------------------------------------------------------------------
// Context bar / plan / permission / tasks
// ---------------------------------------------------------------------------

var (
	ContextChipStyle       = lipgloss.NewStyle().Foreground(Muted)
	ContextChipAccentStyle = lipgloss.NewStyle().Foreground(Brand)
	ContextChipPathStyle   = lipgloss.NewStyle().Foreground(BrandSoft)
	ContextChipTeamStyle   = lipgloss.NewStyle().Foreground(RoleTeammate)
	// contextChipSeparatorStyle paints the vertical bar between chips. Dim
	// foreground keeps the bar present but not loud — it should feel like
	// negative space, not another chip.
	contextChipSeparatorStyle = lipgloss.NewStyle().Foreground(Muted).Faint(true)
	// Transient hints like "Press Ctrl+C again to exit" or "bash mode" — muted
	// gray, not a loud warning.
	ContextChipWarnStyle = lipgloss.NewStyle().Foreground(Muted)

	SubagentCardStyle = card(Accent)

	// TranscriptTitleStyle is the header row of the teammate-transcript
	// modal. Painted with RoleTeammate (purple — the same token used for
	// teammate chips elsewhere) over SurfaceAccent (the same low-contrast
	// strip the user-echo row uses) so the title reads as "you are now
	// observing a teammate" at a glance without shouting. Padding adds a
	// single-column gutter inside the band; the caller fills .Width(...)
	// before Render so the strip stretches edge to edge.
	TranscriptTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(RoleTeammate).
				Background(SurfaceAccent).
				Padding(0, 1)

	PermissionTitleStyle = lipgloss.NewStyle().Foreground(Accent).Bold(true)
	AskCardStyle         = card(BrandSoft)

	TagSubtleStyle = lipgloss.NewStyle().Foreground(Text)
)
