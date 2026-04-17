package tui

// Layout tokens — numeric constants shared across render/update/view layers.
// Only values whose intent is non-obvious at the call site or reused in 3+ places
// are collected here. Self-evident numbers (e.g. indent=2, padding=1) stay inline
// to keep the call sites readable.

const (
	// MaxInputLines caps the textarea height when the user types multiple lines.
	MaxInputLines = 8

	// PaletteMaxVisible caps simultaneously visible slash-command suggestions.
	PaletteMaxVisible = 8

	// ToolResultMaxLines caps how many lines of a tool result are shown in scrollback
	// before collapsing into "… +N lines".
	ToolResultMaxLines = 5

	// ToolStreamTailLines is the window (in lines) kept visible while a tool streams
	// live output in the bottom-pinned area.
	ToolStreamTailLines = 8
)
