package panel

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
	"github.com/voocel/codebot/internal/ui/tui/theme"
)

// Permission asks to allow a tool call or hook command.
type Permission struct {
	req    interact.Approval
	reply  chan<- interact.Choice
	opts   []option
	cursor int
}

type option struct {
	label  string
	key    string // its shortcut
	choice interact.Choice
}

// NewPermission returns the panel for req, which answers on reply.
func NewPermission(req interact.Approval, reply chan<- interact.Choice) *Permission {
	opts := []option{{"Yes", "y", interact.AllowOnce}}
	if !req.OnceOnly {
		opts = append(opts,
			option{"Yes, and allow it for this session", "s", interact.AllowSession},
			option{"Yes, and always allow it", "a", interact.AllowAlways},
		)
	}
	opts = append(opts, option{"No", "n", interact.Deny})
	return &Permission{req: req, reply: reply, opts: opts}
}

func (p *Permission) Key() any { return p.reply }

func (p *Permission) Update(msg tea.Msg) (tea.Cmd, bool) {
	k, ok := key(msg)
	if !ok {
		return nil, false
	}
	switch k {
	case "up", "k", "shift+tab":
		p.cursor = (p.cursor + len(p.opts) - 1) % len(p.opts)
	case "down", "j", "tab":
		p.cursor = (p.cursor + 1) % len(p.opts)
	case "enter":
		return p.answer(p.opts[p.cursor].choice)
	case "esc", "ctrl+c":
		return p.answer(interact.Deny)
	default:
		for i, o := range p.opts {
			if k == o.key || k == strconv.Itoa(i+1) {
				return p.answer(o.choice)
			}
		}
	}
	return nil, false
}

func (p *Permission) answer(c interact.Choice) (tea.Cmd, bool) {
	p.reply <- c
	return nil, true
}

func (p *Permission) View(width, height int) string {
	var body []string
	if s := strings.TrimSpace(p.req.Summary); s != "" {
		body = append(body, markdown.Wrap(s, theme.Text, width-2)...)
	}
	if p.req.Warning != "" {
		body = append(body, theme.ErrorText.Render("⚠ "+p.req.Warning))
	}
	if p.req.OutsideRoots {
		body = append(body, theme.WarmText.Render("Reaches outside the workspace"))
	}
	if p.req.Reason != "" {
		body = append(body, markdown.Wrap(p.req.Reason, theme.MutedText, width-2)...)
	}
	// The summary may be long; the choices must show.
	room := height - 2 - len(p.opts) - 1
	if len(body) > room {
		body = append(body[:max(room-1, 0)], theme.SubtleText.Render("…"))
	}
	body = append(body, "")
	for i, o := range p.opts {
		body = append(body, row(strconv.Itoa(i+1)+". "+o.label, i == p.cursor)+theme.FaintText.Render("  "+o.key))
	}
	title := "Allow " + p.req.Tool + "?"
	return frame(title, body, theme.Hint("↑↓", "select", "enter", "confirm", "esc", "deny"), width, height)
}
