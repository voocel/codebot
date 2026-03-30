package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PermitChoice is the user's response to a permission prompt.
type PermitChoice int

const (
	PermitChoiceDeny         PermitChoice = iota
	PermitChoiceAllowOnce                 // allow this invocation only
	PermitChoiceAllowSession              // allow for the rest of the session
	PermitChoiceAllowAlways               // persist to project config
)

// PermissionMsg is sent to the TUI to show a permission confirmation prompt.
type PermissionMsg struct {
	Tool         string
	Command      string
	Reason       string
	Preview      string
	OutsideRoots bool // when true, only AllowOnce and Deny are shown
	RespCh       chan<- PermitChoice
}

// PermissionDismissMsg tells the TUI to close the permission prompt.
type PermissionDismissMsg struct{}

type permissionState struct {
	tool, command, reason string
	preview               string
	outsideRoots          bool
	respCh                chan<- PermitChoice
	options               []struct {
		label  string
		desc   string
		choice PermitChoice
	}
	cursor int
	done   bool
}

var permissionOptionsFull = []struct {
	label  string
	desc   string
	choice PermitChoice
}{
	{"Allow once", "仅本次允许", PermitChoiceAllowOnce},
	{"Allow for session", "本会话不再询问此类命令", PermitChoiceAllowSession},
	{"Always allow", "保存到项目配置，后续不再询问", PermitChoiceAllowAlways},
	{"Deny", "拒绝执行", PermitChoiceDeny},
}

var permissionOptionsRestricted = []struct {
	label  string
	desc   string
	choice PermitChoice
}{
	{"Allow once", "仅本次允许（路径在授权范围外）", PermitChoiceAllowOnce},
	{"Deny", "拒绝执行", PermitChoiceDeny},
}

func initPermission(msg PermissionMsg) *permissionState {
	opts := permissionOptionsFull
	if msg.OutsideRoots {
		opts = permissionOptionsRestricted
	}
	return &permissionState{
		tool:         msg.Tool,
		command:      msg.Command,
		reason:       msg.Reason,
		preview:      msg.Preview,
		outsideRoots: msg.OutsideRoots,
		respCh:       msg.RespCh,
		options:      opts,
	}
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

func renderPermission(s *permissionState) string {
	var b strings.Builder
	labelStyle := askDescStyle
	valueStyle := askOptionInactiveStyle
	activeOptionStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	inactiveOptionStyle := askOptionInactiveStyle

	b.WriteString(PermissionTitleStyle.Render("Permission Required"))
	b.WriteString("\n\n")

	b.WriteString(labelStyle.Render("  Tool:    "))
	b.WriteString(valueStyle.Render(s.tool))
	b.WriteByte('\n')
	b.WriteString(labelStyle.Render("  Command: "))
	cmd := s.command
	if runes := []rune(cmd); len(runes) > 120 {
		cmd = string(runes[:117]) + "..."
	}
	b.WriteString(valueStyle.Render(cmd))
	if s.reason != "" {
		b.WriteByte('\n')
		b.WriteString(labelStyle.Render("  Reason:  "))
		b.WriteString(valueStyle.Render(s.reason))
	}
	if s.preview != "" {
		preview := s.preview
		if runes := []rune(preview); len(runes) > 240 {
			preview = string(runes[:240]) + "..."
		}
		b.WriteByte('\n')
		b.WriteString(labelStyle.Render("  Preview: "))
		b.WriteString(valueStyle.Render(preview))
	}
	b.WriteString("\n\n")

	for i, opt := range s.options {
		num := fmt.Sprintf("%d. ", i+1)
		prefix := "  "
		style := inactiveOptionStyle
		if i == s.cursor {
			prefix = "> "
			style = activeOptionStyle
		}
		b.WriteString(style.Render(prefix + num + opt.label))
		if opt.desc != "" {
			b.WriteString(" ")
			b.WriteString(askDescStyle.Render("(" + opt.desc + ")"))
		}
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(askHintStyle.Render("Enter to select · ↑↓ navigate · Esc to deny"))

	return AskCardStyle.Render(b.String())
}
