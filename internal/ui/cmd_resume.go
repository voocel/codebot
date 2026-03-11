package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/ui/tui"
)

// ResumeCommand implements InteractiveCommand for /resume.
// Opens an interactive overlay to select and switch sessions.
type ResumeCommand struct {
	app   *App
	state *resumeSelectState
}

type resumeSelectState struct {
	sessions []storage.SessionInfo
	cursor   int
	current  string // current session ID
}

func NewResumeCommand(app *App) *ResumeCommand {
	return &ResumeCommand{app: app}
}

func (c *ResumeCommand) Spec() CommandSpec {
	return CommandSpec{
		Name:        "resume",
		Usage:       "/resume",
		Description: "Switch to another session",
		Risk:        policy.RiskMedium,
		NeedsIdle:   true,
		Kind:        CommandKindBuiltin,
	}
}

func (c *ResumeCommand) Run(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
	sessions, err := ctx.App.Session.ListSessions()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to list sessions: " + err.Error()))
	}
	if len(sessions) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("No sessions found."))
	}

	var currentID string
	if info, err := ctx.App.Session.CurrentSessionInfo(); err == nil {
		currentID = info.ID
	}

	// Position cursor on current session.
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
	ctx.App.registry.SetOverlay(c)
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
		c.app.registry.ClearOverlay()

		if selected.ID == s.current {
			return true, tui.SendCommandResult(tui.CommandStyle.Render("Already in this session."))
		}

		if err := c.app.Session.SwitchSession(selected.ID); err != nil {
			return true, tui.SendCommandResult(tui.ErrorStyle.Render("Failed to switch session: " + err.Error()))
		}
		c.app.resetPlanState()

		// Return RestoreMsg directly as a cmd instead of relying on
		// the SESessionSwitched event via p.Send (which deadlocks because
		// p.Send blocks when called synchronously inside bubbletea's Update).
		msgs := c.app.Session.Messages()
		return true, func() tea.Msg { return tui.RestoreMsg{Msgs: msgs} }

	case "esc", "ctrl+c":
		c.app.registry.ClearOverlay()
		return true, nil
	}

	return true, nil
}

func (c *ResumeCommand) View(width int) string {
	if c.state == nil {
		return ""
	}
	s := c.state

	var sb strings.Builder
	sb.WriteString(tui.MutedStyle.Render("Select session (↑↓ navigate · Enter select · Esc cancel):"))
	sb.WriteString("\n")

	selectedStyle := lipgloss.NewStyle().Foreground(tui.ColorAccent).Bold(true)
	currentMark := lipgloss.NewStyle().Foreground(tui.ColorSuccess)

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
		if sess.Name != "" {
			name = sess.Name
		}

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
