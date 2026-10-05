package commands

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui"
)

// ResumeCommand drives /resume — an interactive overlay listing recent
// sessions and switching to the chosen one.
type ResumeCommand struct {
	app     *app.App
	overlay OverlayController

	state *resumeSelectState
}

type resumeSelectState struct {
	sessions []app.SessionInfo
	cursor   int
	current  string
}

// Resume constructs the /resume command.
func Resume(a *app.App, overlay OverlayController) *ResumeCommand {
	return &ResumeCommand{app: a, overlay: overlay}
}

func (c *ResumeCommand) Spec() Spec {
	return Spec{
		Name:        "resume",
		Usage:       "/resume",
		Description: "Switch to another session",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}
}

func (c *ResumeCommand) Run(_ Invocation) tea.Cmd {
	sessions, err := c.app.Sessions()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to list sessions: " + err.Error()))
	}
	if len(sessions) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("No sessions found."))
	}
	currentID := c.app.Current().ID()

	cursor := 0
	for i, s := range sessions {
		if s.ID == currentID {
			cursor = i
			break
		}
	}

	c.state = &resumeSelectState{
		sessions: sessions,
		cursor:   cursor,
		current:  currentID,
	}
	c.overlay.SetOverlay(c)
	return nil
}

func (c *ResumeCommand) Active() bool { return c.state != nil }

func (c *ResumeCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if c.state == nil {
		return false, nil
	}
	s := c.state

	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
		return true, nil

	case "down", "j":
		limit := min(len(s.sessions), 15)
		if s.cursor < limit-1 {
			s.cursor++
		}
		return true, nil

	case "enter":
		selected := s.sessions[s.cursor]
		c.overlay.ClearOverlay()

		if selected.ID == s.current {
			return true, tui.SendCommandResult(tui.CommandStyle.Render("Already in this session."))
		}
		// Off the TUI's goroutine: closing the current conversation waits for
		// its run, and opening publishes to the TUI.
		return true, func() tea.Msg {
			if _, err := c.app.Open(selected.ID); err != nil {
				return tui.CommandResultMsg{Text: tui.ErrorStyle.Render("Failed to switch session: " + err.Error())}
			}
			return nil
		}

	case "esc", "ctrl+c":
		c.overlay.ClearOverlay()
		return true, nil
	}

	return true, nil
}

func (c *ResumeCommand) View(width, _ int) string {
	if c.state == nil {
		return ""
	}
	s := c.state

	var sb strings.Builder
	sb.WriteString(tui.MutedStyle.Render("Select session (↑↓ navigate · Enter select · Esc cancel):"))
	sb.WriteString("\n")

	selectedStyle := lipgloss.NewStyle().Foreground(tui.Accent).Bold(true)
	currentMark := lipgloss.NewStyle().Foreground(tui.Success)

	limit := min(len(s.sessions), 15)
	for i := range limit {
		sess := s.sessions[i]

		prefix := "  "
		if i == s.cursor {
			prefix = "> "
		}

		marker := "  "
		if sess.ID == s.current {
			marker = "* "
		}

		name := sess.ID
		line := fmt.Sprintf("%s%s%-20s (%d msgs)  %s",
			prefix, marker, name, sess.MessageCount, sess.Updated.Format("01-02 15:04"))
		if sess.FirstMessage != "" {
			msg := sess.FirstMessage
			maxLen := max(0, width-len(line)-5)
			if maxLen > 0 && len(msg) > maxLen {
				msg = msg[:maxLen-3] + "..."
			}
			if maxLen > 0 {
				line += "  " + msg
			}
		}

		if i == s.cursor {
			sb.WriteString(selectedStyle.Render(line))
		} else if sess.ID == s.current {
			sb.WriteString(currentMark.Render(line))
		} else {
			sb.WriteString(tui.MutedStyle.Render(line))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (c *ResumeCommand) Dismiss() {
	c.state = nil
}
