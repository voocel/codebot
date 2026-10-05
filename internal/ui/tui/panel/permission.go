package panel

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
	"github.com/voocel/codebot/internal/ui/tui/syntax"
	"github.com/voocel/codebot/internal/ui/tui/theme"
	"github.com/voocel/codebot/internal/ui/tui/transcript"
)

// Permission asks to allow a tool call or hook command.
type Permission struct {
	req   interact.Approval
	reply chan<- interact.Verdict
	opts  []option
	menu  menu
	head  head
	queue
	instead field // what to do instead, with the last option
}

type option struct {
	label  string
	choice interact.Choice
	// first is done before the answer goes, so that it holds for the
	// calls after this one.
	first func()
}

// NewPermission returns the panel for req, which answers on reply.
// acceptEdits switches to the accept-edits mode, offered for an edit.
func NewPermission(req interact.Approval, reply chan<- interact.Verdict, acceptEdits func()) *Permission {
	opts := []option{{label: "Yes", choice: interact.AllowOnce}}
	if req.Remember != "" {
		opts = append(opts, option{label: "Yes, and don't ask again for " + req.Remember, choice: interact.AllowAlways})
	}
	if req.Edit {
		opts = append(opts, option{label: "Yes, and allow all edits this session", choice: interact.AllowOnce, first: acceptEdits})
	}
	opts = append(opts, option{label: "No, and tell codebot what to do instead", choice: interact.Deny})
	return &Permission{req: req, reply: reply, opts: opts, menu: menu{n: len(opts)}, instead: newField("What should codebot do instead?")}
}

func (p *Permission) Key() any { return p.reply }

func (p *Permission) Update(msg tea.Msg) (tea.Cmd, bool) {
	k, _ := key(msg)
	if p.instead.on {
		if k == "ctrl+c" {
			return p.answer(interact.Verdict{Choice: interact.Deny})
		}
		cmd, text, entered := p.instead.update(msg)
		if entered {
			return p.answer(interact.Verdict{Choice: interact.Deny, Feedback: text})
		}
		return cmd, false
	}
	if k == "" || p.head.key(k) {
		return nil, false
	}
	if k == "esc" || k == "ctrl+c" {
		return p.answer(interact.Verdict{Choice: interact.Deny})
	}
	i := p.menu.key(k)
	switch {
	case i < 0:
		return nil, false
	case i == len(p.opts)-1:
		return p.instead.open(""), false
	}
	if o := p.opts[i]; o.first != nil {
		o.first()
	}
	return p.answer(interact.Verdict{Choice: p.opts[i].choice})
}

func (p *Permission) answer(v interact.Verdict) (tea.Cmd, bool) {
	p.reply <- v
	return nil, true
}

func (p *Permission) View(width, height int) string {
	var lines []string
	if p.req.Warning != "" {
		lines = append(lines, markdown.Wrap("⚠ "+p.req.Warning, theme.ErrorText, width-2)...)
	}
	lines = append(lines, p.summary(width-2)...)
	// Why a call is asked each time; the usual why is the mode's.
	switch {
	case p.req.OutsideRoots:
		lines = append(lines, theme.WarmText.Render("Reaches outside the workspace · asked each time"))
	case p.req.Confirm:
		lines = append(lines, markdown.Wrap(p.req.Reason, theme.WarmText, width-2)...)
	}
	var opts []string
	for i, o := range p.opts {
		opts = append(opts, p.menu.numbered(i, o.label))
	}
	if p.instead.on {
		opts[len(opts)-1] = p.instead.view(width)
	}
	body, cut := p.head.fit(lines, opts, height-2)
	var hint string
	switch {
	case p.instead.on:
		hint = theme.Hint("enter", "deny and send", "esc", "back")
	case cut:
		hint = theme.Hint("↑↓", "select", "enter", "confirm", "pgup/pgdn", "scroll", "esc", "deny")
	default:
		hint = theme.Hint("↑↓", "select", "enter", "confirm", "esc", "deny")
	}
	return frame(p.queue.title("Allow "+transcript.Title(p.req.Tool)+"?"), body, hint, width, height)
}

// summary shows what the call does: a command highlighted as one, a file
// from the workspace.
func (p *Permission) summary(width int) []string {
	s := strings.TrimSpace(p.req.Summary)
	switch p.req.Tool {
	case "bash":
	case "read", "write", "edit":
		return markdown.Wrap(transcript.ShortPath(s), theme.Text, width)
	default:
		return markdown.Wrap(s, theme.Text, width)
	}
	var out []string
	for _, l := range strings.Split(syntax.Lang(s, "bash"), "\n") {
		out = append(out, strings.Split(ansi.Wrap(l, width, ""), "\n")...)
	}
	return out
}
