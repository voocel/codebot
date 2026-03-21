package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
	Tool    string
	Command string
	Reason  string
	Preview string
	RespCh  chan<- PermitChoice
}

// PermissionDismissMsg tells the TUI to close the permission prompt.
type PermissionDismissMsg struct{}

type permissionState struct {
	tool, command, reason string
	preview               string
	respCh                chan<- PermitChoice
	cursor                int // 0=AllowOnce, 1=AllowSession, 2=Deny
	done                  bool
}

var permissionOptions = []struct {
	label  string
	desc   string
	choice PermitChoice
}{
	{"Allow once", "仅本次允许", PermitChoiceAllowOnce},
	{"Allow for session", "本会话不再询问此类命令", PermitChoiceAllowSession},
	{"Always allow", "保存到项目配置，后续不再询问", PermitChoiceAllowAlways},
	{"Deny", "拒绝执行", PermitChoiceDeny},
}

func initPermission(msg PermissionMsg) *permissionState {
	return &permissionState{
		tool:    msg.Tool,
		command: msg.Command,
		reason:  msg.Reason,
		preview: msg.Preview,
		respCh:  msg.RespCh,
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
		if s.cursor < len(permissionOptions)-1 {
			s.cursor++
		}
		return true, nil
	case "enter":
		s.respCh <- permissionOptions[s.cursor].choice
		s.done = true
		return true, nil
	case "1":
		s.respCh <- PermitChoiceAllowOnce
		s.done = true
		return true, nil
	case "2":
		s.respCh <- PermitChoiceAllowSession
		s.done = true
		return true, nil
	case "3":
		s.respCh <- PermitChoiceAllowAlways
		s.done = true
		return true, nil
	case "4":
		s.respCh <- PermitChoiceDeny
		s.done = true
		return true, nil
	default:
		return true, nil // absorb all other keys
	}
}

func renderPermission(s *permissionState) string {
	var b strings.Builder

	b.WriteString(askQuestionStyle.Render("⚠ Permission Required"))
	b.WriteString("\n\n")

	b.WriteString(askDescStyle.Render("  Tool:    "))
	b.WriteString(askOptionInactiveStyle.Render(s.tool))
	b.WriteByte('\n')
	b.WriteString(askDescStyle.Render("  Command: "))
	cmd := s.command
	if runes := []rune(cmd); len(runes) > 120 {
		cmd = string(runes[:117]) + "..."
	}
	b.WriteString(askOptionInactiveStyle.Render(cmd))
	if s.reason != "" {
		b.WriteByte('\n')
		b.WriteString(askDescStyle.Render("  Reason:  "))
		b.WriteString(askOptionInactiveStyle.Render(s.reason))
	}
	if s.preview != "" {
		preview := s.preview
		if runes := []rune(preview); len(runes) > 240 {
			preview = string(runes[:240]) + "..."
		}
		b.WriteByte('\n')
		b.WriteString(askDescStyle.Render("  Preview: "))
		b.WriteString(askOptionInactiveStyle.Render(preview))
	}
	b.WriteString("\n\n")

	for i, opt := range permissionOptions {
		num := fmt.Sprintf("%d. ", i+1)
		label := num + opt.label
		if i == s.cursor {
			b.WriteString(askOptionActiveStyle.Render("> " + label))
		} else {
			b.WriteString(askOptionInactiveStyle.Render("  " + label))
		}
		b.WriteByte('\n')
		b.WriteString(askDescStyle.Render("     " + opt.desc))
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	b.WriteString(askHintStyle.Render("Enter to select · ↑↓ Navigate · Esc to deny"))

	return indentBlock(b.String(), 2)
}
