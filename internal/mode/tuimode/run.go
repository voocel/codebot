package tuimode

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/tui"
)

// Run executes interactive TUI mode.
func Run(sess *agent.AgentSession, cwd, gitBranch, modelName string, profile policy.Profile) error {
	ui := &app.App{
		Session:       sess,
		Cwd:           cwd,
		GitBranch:     gitBranch,
		PolicyProfile: profile,
	}

	m := tui.New(sess, modelName, ui.Config())
	// Use the normal terminal screen so users keep native scrollback history.
	p := tea.NewProgram(m)

	unsub := sess.Subscribe(func(ev agent.SessionEvent) {
		if ev.Type == agent.SEAgentEvent && ev.AgentEvent != nil {
			p.Send(tui.AgentEventMsg{Event: *ev.AgentEvent})
			return
		}
		if ev.Type == agent.SEError && ev.Error != nil {
			p.Send(tui.CommandResultMsg{
				Text: tui.ErrorStyle.Render("Session error: " + ev.Error.Error()),
			})
		}
	})
	defer unsub()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}
	return nil
}
