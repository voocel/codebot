package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/ui/tui"
)

// RunTUI executes interactive TUI mode.
func RunTUI(sess *agent.Session, cwd, gitBranch, modelName string, profile policy.Profile, mcpMgr *mcpclient.Manager) error {
	adapter := &App{
		Session:       sess,
		Cwd:           cwd,
		GitBranch:     gitBranch,
		PolicyProfile: profile,
		Templates:     config.LoadPromptTemplates(cwd),
		Skills:        sess.Skills(),
		PlanStore:     storage.NewPlanStore(config.PlansDir(cwd)),
		MCPManager:    mcpMgr,
	}

	m := tui.New(sess, modelName, adapter.Config())
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
