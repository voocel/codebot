package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/interact"
)

// PermissionMsg shows a request for consent.
type PermissionMsg struct {
	Approval interact.Approval
	RespCh   chan<- interact.Choice
}

// PermissionDismissMsg closes a pending permission prompt whose asker stopped
// waiting, while it is still queued or on screen. RespCh identifies the card.
type PermissionDismissMsg struct {
	RespCh chan<- interact.Choice
}

type permissionOption struct {
	label  string
	desc   string
	choice interact.Choice
}

type permissionState struct {
	req     interact.Approval
	respCh  chan<- interact.Choice
	options []permissionOption
	cursor  int
	done    bool
}

var permissionOptionsFull = []permissionOption{
	{"Allow once", "this invocation only", interact.AllowOnce},
	{"Allow for session", "don't ask again this session", interact.AllowSession},
	{"Always allow", "save to project config", interact.AllowAlways},
	{"Deny", "reject this invocation", interact.Deny},
}

// permissionOptionsOnce is offered when only a one-time allow may be: the
// request's Reason says why.
var permissionOptionsOnce = []permissionOption{
	{"Allow once", "this invocation only", interact.AllowOnce},
	{"Deny", "reject this invocation", interact.Deny},
}

func initPermission(msg PermissionMsg) *permissionState {
	opts := permissionOptionsFull
	if msg.Approval.OnceOnly {
		opts = permissionOptionsOnce
	}
	return &permissionState{req: msg.Approval, respCh: msg.RespCh, options: opts}
}

// dialogCard implementation.

func (s *permissionState) key() any       { return s.respCh }
func (s *permissionState) finished() bool { return s.done }

// abort answers Deny on behalf of the user. The response channel is buffered,
// so this never blocks even when the asker already gave up on the answer.
func (s *permissionState) abort() {
	if s.done {
		return
	}
	s.done = true
	s.respCh <- interact.Deny
}

func (s *permissionState) render(m *Model) string { return renderPermission(s, m.Width) }

// hidesContextBar: permission cards are compact; the context bar stays.
func (s *permissionState) hidesContextBar() bool { return false }

func (s *permissionState) handleKey(_ *Model, msg tea.KeyMsg) (bool, tea.Cmd) {
	if msg.String() == "ctrl+c" || msg.String() == "esc" {
		s.abort()
		return true, nil
	}
	return handlePermissionKey(s, msg)
}

func handlePermissionKey(s *permissionState, msg tea.KeyMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
		return true, nil
	case "down", "j":
		if s.cursor < len(s.options)-1 {
			s.cursor++
		}
		return true, nil
	case "enter":
		s.respCh <- s.options[s.cursor].choice
		s.done = true
		return true, nil
	default:
		// Number keys: "1" .. "N" for quick select.
		if len(msg.String()) == 1 && msg.String()[0] >= '1' {
			idx := int(msg.String()[0] - '1')
			if idx < len(s.options) {
				s.respCh <- s.options[idx].choice
				s.done = true
			}
		}
		return true, nil // absorb all other keys
	}
}

// renderPermission renders the card at width; the command is shown whole,
// as the approver must see everything they approve.
func renderPermission(s *permissionState, width int) string {
	var b strings.Builder
	label := askDescStyle
	value := askOptionInactiveStyle

	b.WriteString(PermissionTitleStyle.Render("Permission Required"))
	b.WriteString("\n\n")

	b.WriteString(label.Render("  Tool:    "))
	b.WriteString(value.Render(s.req.Tool))
	b.WriteByte('\n')
	b.WriteString(label.Render("  Command: "))
	const indent = len("  Command: ")
	cmd := wrapTextWidth(s.req.Summary, width-indent-1)
	b.WriteString(value.Render(strings.ReplaceAll(cmd, "\n", "\n"+strings.Repeat(" ", indent))))
	if s.req.Warning != "" {
		b.WriteByte('\n')
		b.WriteString(lipgloss.NewStyle().Foreground(Accent).Bold(true).Render("  Warning: " + s.req.Warning))
	}
	if s.req.Reason != "" {
		b.WriteByte('\n')
		b.WriteString(label.Render("  Reason:  "))
		b.WriteString(value.Render(s.req.Reason))
	}
	b.WriteString("\n\n")

	active := lipgloss.NewStyle().Foreground(Accent).Bold(true)
	for i, opt := range s.options {
		prefix, style := "  ", askOptionInactiveStyle
		if i == s.cursor {
			prefix, style = "> ", active
		}
		b.WriteString(style.Render(fmt.Sprintf("%s%d. %s", prefix, i+1, opt.label)))
		b.WriteString(" ")
		b.WriteString(askDescStyle.Render("(" + opt.desc + ")"))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(askHintStyle.Render("Enter to select · ↑↓ navigate · Esc to deny"))

	return AskCardStyle.Render(b.String())
}
