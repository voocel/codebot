package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/ui/tui/markdown"
)

// planExitToolName is the codebot tool that requests user approval of a
// finished plan. Plan-mode approval has different UX requirements than a
// generic permission prompt (must show full plan content, only two choices,
// markdown rendering), so the renderer special-cases this tool.
const planExitToolName = "exit_plan_mode"

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
	{"Allow once", "this invocation only", PermitChoiceAllowOnce},
	{"Allow for session", "don't ask again this session", PermitChoiceAllowSession},
	{"Always allow", "save to project config", PermitChoiceAllowAlways},
	{"Deny", "reject this invocation", PermitChoiceDeny},
}

var permissionOptionsRestricted = []struct {
	label  string
	desc   string
	choice PermitChoice
}{
	{"Allow once", "path outside authorized roots", PermitChoiceAllowOnce},
	{"Deny", "reject this invocation", PermitChoiceDeny},
}

// Plan approval is binary by nature (a plan is approved once or refined and
// retried), so AllowSession / AllowAlways have no meaning here.
var permissionOptionsPlanExit = []struct {
	label  string
	desc   string
	choice PermitChoice
}{
	{"Approve plan", "leave plan mode and start execution", PermitChoiceAllowOnce},
	{"Reject plan", "stay in plan mode and refine", PermitChoiceDeny},
}

func initPermission(msg PermissionMsg) *permissionState {
	opts := permissionOptionsFull
	switch {
	case msg.Tool == planExitToolName:
		opts = permissionOptionsPlanExit
	case msg.OutsideRoots:
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

func renderPermission(s *permissionState, md *markdown.Renderer) string {
	if s.tool == planExitToolName {
		return renderPlanApproval(s, md)
	}

	var b strings.Builder
	labelStyle := askDescStyle
	valueStyle := askOptionInactiveStyle
	activeOptionStyle := lipgloss.NewStyle().Foreground(Accent).Bold(true)
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
		// Render the full preview. agentcore's previewText already caps generic
		// tool previews at 400 chars; any further cap here would hide content
		// from the approver.
		b.WriteByte('\n')
		b.WriteString(labelStyle.Render("  Preview:\n"))
		b.WriteString(valueStyle.Render(s.preview))
	}
	b.WriteString("\n\n")

	renderOptions(&b, s, activeOptionStyle, inactiveOptionStyle)

	b.WriteByte('\n')
	b.WriteString(askHintStyle.Render("Enter to select · ↑↓ navigate · Esc to deny"))

	return AskCardStyle.Render(b.String())
}

// renderPlanApproval keeps the card compact (title + 2 options) so it always
// fits at the bottom of the terminal. The full plan body is pushed into
// scrollback as a separate block when the PermissionMsg arrives — see
// renderPlanScrollback. Trying to render the entire plan inside the live
// View card causes it to overflow the terminal height and clip silently
// (bubbletea cannot scroll a live View).
func renderPlanApproval(s *permissionState, _ *markdown.Renderer) string {
	var b strings.Builder
	activeOptionStyle := lipgloss.NewStyle().Foreground(Accent).Bold(true)
	inactiveOptionStyle := askOptionInactiveStyle

	b.WriteString(PermissionTitleStyle.Render("Plan Ready for Approval"))
	b.WriteString("\n")
	b.WriteString(MutedStyle.Render("Plan printed above · approve to start execution, reject to keep planning."))
	b.WriteString("\n\n")

	renderOptions(&b, s, activeOptionStyle, inactiveOptionStyle)

	b.WriteByte('\n')
	b.WriteString(askHintStyle.Render("Enter to confirm · ↑↓ navigate · Esc to reject"))

	return AskCardStyle.Render(b.String())
}

// renderPlanScrollback formats the plan body as a scrollback block so it
// stays visible (and scrollable in the user's terminal) regardless of plan
// length. Called once when the plan-approval prompt is opened. Mirrors CC's
// PlanApprovalRequestDisplay: a header, then the plan body fenced by top/
// bottom rules and indented two columns so it reads as a self-contained
// section rather than a wall of text flush against the margin.
func renderPlanScrollback(plan string, md *markdown.Renderer, width int) string {
	body := strings.TrimSpace(plan)
	if body == "" {
		body = "(no plan content)"
	} else if md != nil {
		body = strings.TrimSpace(md.RenderFinal(body))
	}
	body = indentBlock(body, 2)

	ruleWidth := width - 4
	if ruleWidth < 20 {
		ruleWidth = 20
	}
	rule := MutedStyle.Render(strings.Repeat("─", ruleWidth))

	header := PermissionTitleStyle.Render("Plan Ready for Approval")
	return strings.Join([]string{header, "", rule, body, rule}, "\n")
}

func renderOptions(b *strings.Builder, s *permissionState, active, inactive lipgloss.Style) {
	for i, opt := range s.options {
		num := fmt.Sprintf("%d. ", i+1)
		prefix := "  "
		style := inactive
		if i == s.cursor {
			prefix = "> "
			style = active
		}
		b.WriteString(style.Render(prefix + num + opt.label))
		if opt.desc != "" {
			b.WriteString(" ")
			b.WriteString(askDescStyle.Render("(" + opt.desc + ")"))
		}
		b.WriteByte('\n')
	}
}
