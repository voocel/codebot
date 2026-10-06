package panel

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// Consent asks the user to agree to what would run as them: a folder's
// surface, or a plugin's to add or update.
type Consent struct {
	key         any
	title, lead string
	items       app.Surface
	choices     []Choice
	esc         func() tea.Cmd
	menu        menu
	head        head
	queue
}

// Choice is an option of a Consent.
type Choice struct {
	Label string
	Pick  func() tea.Cmd
}

// NewConsent returns the panel asking, under title, to agree to items, told
// by lead. key identifies it; esc runs as the user dismisses it.
func NewConsent(key any, title, lead string, items app.Surface, choices []Choice, esc func() tea.Cmd) *Consent {
	return &Consent{key: key, title: title, lead: lead, items: items, choices: choices, esc: esc, menu: menu{n: len(choices)}}
}

func (p *Consent) Key() any { return p.key }

func (p *Consent) Update(msg tea.Msg) (tea.Cmd, bool) {
	k, ok := key(msg)
	if !ok || p.head.key(k) {
		return nil, false
	}
	if k == "esc" || k == "ctrl+c" {
		return p.esc(), true
	}
	i := p.menu.key(k)
	if i < 0 {
		return nil, false
	}
	return p.choices[i].Pick(), true
}

// kinds name the kinds of a surface's items.
var kinds = map[string]string{
	"hook":   "hook",
	"mcp":    "MCP server",
	"allow":  "allows",
	"read":   "reads",
	"write":  "writes",
	"skill":  "skill",
	"plugin": "plugin",
}

func (p *Consent) View(width, height int) string {
	lines := markdown.Wrap(p.lead, theme.Text, width-2)
	for _, it := range p.items {
		label := kinds[it.Kind]
		pad := max(12-ansi.StringWidth(label), 1)
		for i, d := range markdown.Wrap(it.Detail, theme.Text, max(width-16, 10)) {
			if i == 0 {
				d = theme.MutedText.Render("  "+label) + strings.Repeat(" ", pad) + d
			} else {
				d = strings.Repeat(" ", 14) + d
			}
			lines = append(lines, d)
		}
	}
	if len(p.items) > 0 {
		lines = append(lines, theme.WarmText.Render("They run as you, outside any sandbox."))
	}
	var opts []string
	for i, c := range p.choices {
		opts = append(opts, p.menu.numbered(i, c.Label))
	}
	body, cut := p.head.fit(lines, opts, height-2)
	hint := theme.Hint("↑↓", "select", "enter", "confirm", "esc", "decide later")
	if cut {
		hint = theme.Hint("↑↓", "select", "enter", "confirm", "pgup/pgdn", "scroll", "esc", "decide later")
	}
	return frame(p.queue.title(p.title), body, hint, width, height)
}
