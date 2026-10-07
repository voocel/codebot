package panel

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// Consent asks about items that would run as the user, from a folder or a
// plugin. Checked items are agreed to, the rest declined.
type Consent struct {
	key         any
	title, lead string
	items       app.Surface
	checked     []bool
	choices     []Choice
	esc         func() tea.Cmd
	at          int // cursor over the items, then the choices
	head        head
	queue
}

// Pick receives the checked items.
type Choice struct {
	Label string
	Pick  func(checked app.Surface) tea.Cmd
}

// NewConsent starts with checked items checked. key identifies the request;
// esc runs when the user dismisses it.
func NewConsent(key any, title, lead string, items, checked app.Surface, choices []Choice, esc func() tea.Cmd) *Consent {
	p := &Consent{key: key, title: title, lead: lead, items: items, checked: make([]bool, len(items)), choices: choices, esc: esc, at: len(items)}
	for i, it := range items {
		p.checked[i] = checked.Has(it)
	}
	return p
}

func (p *Consent) Key() any { return p.key }

func (p *Consent) Update(msg tea.Msg) (tea.Cmd, bool) {
	k, ok := key(msg)
	if !ok || p.head.key(k) {
		return nil, false
	}
	n := len(p.items) + len(p.choices)
	switch k {
	case "esc", "ctrl+c":
		return p.esc(), true
	case "up", "k":
		p.at = (p.at + n - 1) % n
	case "down", "j":
		p.at = (p.at + 1) % n
	case "space", "enter":
		if p.at < len(p.items) {
			p.checked[p.at] = !p.checked[p.at]
		} else if k == "enter" {
			return p.choices[p.at-len(p.items)].Pick(p.agreed()), true
		}
	default:
		if i, err := strconv.Atoi(k); err == nil && i >= 1 && i <= len(p.choices) {
			return p.choices[i-1].Pick(p.agreed()), true
		}
	}
	return nil, false
}

func (p *Consent) agreed() app.Surface {
	var out app.Surface
	for i, it := range p.items {
		if p.checked[i] {
			out = append(out, it)
		}
	}
	return out
}

var kinds = map[string]string{
	"hook":   "hook",
	"mcp":    "MCP server",
	"allow":  "allows",
	"read":   "reads",
	"write":  "writes",
	"skill":  "skill",
	"plugin": "plugin",
}

func KindLabel(kind string) string { return kinds[kind] }

func (p *Consent) View(width, height int) string {
	lines := markdown.Wrap(p.lead, theme.Text, width-2)
	for i, it := range p.items {
		box := "[ ] "
		if p.checked[i] {
			box = "[x] "
		}
		label := box + kinds[it.Kind]
		pad := max(16-ansi.StringWidth(label), 1)
		if i == p.at {
			p.head.follow(len(lines))
		}
		for j, d := range markdown.Wrap(it.Detail, theme.Text, max(width-20, 10)) {
			switch {
			case j > 0:
				d = strings.Repeat(" ", 18) + d
			case i == p.at:
				d = theme.Selected.Render("❯ "+label) + strings.Repeat(" ", pad) + d
			default:
				d = "  " + theme.MutedText.Render(label) + strings.Repeat(" ", pad) + d
			}
			lines = append(lines, d)
		}
	}
	if len(p.items) > 0 {
		lines = append(lines, theme.WarmText.Render("They run as you, outside any sandbox."))
	}
	var opts []string
	for i, c := range p.choices {
		opts = append(opts, row(strconv.Itoa(i+1)+". "+c.Label, p.at == len(p.items)+i))
	}
	body, cut := p.head.fit(lines, opts, height-2)
	keys := []string{"↑↓", "select"}
	if len(p.items) > 0 {
		keys = append(keys, "space", "check")
	}
	keys = append(keys, "enter", "confirm")
	if cut {
		keys = append(keys, "pgup/pgdn", "scroll")
	}
	hint := theme.Hint(append(keys, "esc", "decide later")...)
	return frame(p.queue.title(p.title), body, hint, width, height)
}
