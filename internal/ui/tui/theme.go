package tui

// theme.go — semantic color tokens, adaptive for light/dark terminals.
//
// Design principle: tokens express *role* ("subtle hint", "danger"), not
// absolute color. Each token holds both a Light and Dark value; lipgloss
// resolves at render time via terminal background detection (OSC 11 query).
//
// Override: CODEBOT_THEME=light|dark forces the choice for terminals that
// report background color incorrectly.

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Foundation — text scale (recedes left-to-right: Strong > Text > Muted > Subtle > Meta).
var (
	Strong = lipgloss.AdaptiveColor{Light: "#000000", Dark: "#FFFFFF"} // highest contrast — bullets, critical anchors
	Text   = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}         // body text
	Muted  = lipgloss.AdaptiveColor{Light: "242", Dark: "247"}         // secondary labels, status
	Subtle = lipgloss.AdaptiveColor{Light: "246", Dark: "243"}         // placeholder, thinking, hints
	Meta   = lipgloss.AdaptiveColor{Light: "243", Dark: "246"}         // line numbers, tails, dim metadata
)

// Chrome — borders, separators, titles, rules.
var (
	Border    = lipgloss.AdaptiveColor{Light: "247", Dark: "241"}
	Separator = lipgloss.AdaptiveColor{Light: "248", Dark: "242"}
	Title     = lipgloss.AdaptiveColor{Light: "235", Dark: "255"}
	InputRule = lipgloss.AdaptiveColor{Light: "245", Dark: "244"}
)

// Surfaces — background tints (foreground colors for backgrounds).
var (
	SurfaceAccent = lipgloss.AdaptiveColor{Light: "254", Dark: "236"} // user echo strip
)

// Brand — teal, primary action.
var (
	Brand     = lipgloss.AdaptiveColor{Light: "#2B7B70", Dark: "#3FA796"}
	BrandSoft = lipgloss.AdaptiveColor{Light: "30", Dark: "72"} // muted teal for borders/labels
)

// Accent — amber, emphasis & tool surfaces.
var Accent = lipgloss.AdaptiveColor{Light: "#B47A2E", Dark: "#E5B567"}

// Status.
var (
	Success = lipgloss.AdaptiveColor{Light: "#2C7A39", Dark: "#4EBA65"} // pure saturated green
	Danger  = lipgloss.AdaptiveColor{Light: "#C53030", Dark: "#E06C75"}
	Info    = lipgloss.AdaptiveColor{Light: "#1E6FAF", Dark: "#78C6E7"}
	Live    = lipgloss.AdaptiveColor{Light: "31", Dark: "153"} // spinner / running chrome
)

// Diff backgrounds — low-saturation tints for full-line fills, with a deeper
// shade reserved for word-level intra-line emphasis. Keeping these as
// background-only lets the body's existing foreground (syntax highlighting,
// path tokens, etc.) survive intact.
var (
	DiffAddBg          = lipgloss.AdaptiveColor{Light: "#DAFBE1", Dark: "#0E2A1A"}
	DiffRemoveBg       = lipgloss.AdaptiveColor{Light: "#FFEBE9", Dark: "#3A1416"}
	DiffAddBgStrong    = lipgloss.AdaptiveColor{Light: "#AAEBC1", Dark: "#1F5D33"}
	DiffRemoveBgStrong = lipgloss.AdaptiveColor{Light: "#FFC1BC", Dark: "#7A1E22"}
)

// Message roles.
var (
	RoleUser      = lipgloss.AdaptiveColor{Light: "31", Dark: "#9CC2F9"}
	RoleAssistant = lipgloss.AdaptiveColor{Light: "#3A6F6B", Dark: "#B8E1DD"}
	RoleShell     = lipgloss.AdaptiveColor{Light: "#A04870", Dark: "#D16D9E"}
)

// Highlights.
var Path = lipgloss.AdaptiveColor{Light: "26", Dark: "111"} // file / path token

// init allows CODEBOT_THEME=light|dark to force theme resolution for
// terminals whose background query is unreliable. lipgloss resolves
// AdaptiveColor lazily at render time, so the override applies globally.
func init() {
	switch strings.ToLower(os.Getenv("CODEBOT_THEME")) {
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	}
}
