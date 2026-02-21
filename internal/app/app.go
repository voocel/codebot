package app

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/tui"
)

// App is the TUI adapter. Business logic lives in AgentSession.
type App struct {
	Session   *agent.AgentSession
	Cwd       string
	GitBranch string

	// PolicyProfile controls slash-command risk gating.
	PolicyProfile policy.Profile

	// Templates are user-defined prompt templates loaded from .md files.
	Templates []config.PromptTemplate

	// Plan mode state.
	planState planState
	planSteps []planStep
}

// Config returns a tui.Config with all hooks wired to this App.
func (a *App) Config() tui.Config {
	return tui.Config{
		Cwd:         a.Cwd,
		GitBranch:   a.GitBranch,
		OnKey:       a.onKey(),
		OnEvent:     a.planOnEvent,
		StatusRight: a.statusRight,
		OnFooter:    a.planFooter,
	}
}

// onKey returns a hook that intercepts slash commands.
func (a *App) onKey() func(m *tui.Model, msg tea.KeyMsg) (bool, tea.Cmd) {
	return func(m *tui.Model, msg tea.KeyMsg) (bool, tea.Cmd) {
		if msg.String() != "enter" {
			return false, nil
		}
		text := strings.TrimSpace(m.Input.Value())
		if !strings.HasPrefix(text, "/") {
			return false, nil
		}
		m.Input.Reset()
		return true, a.handleCommand(text)
	}
}

// statusRight displays context usage in the status bar.
func (a *App) statusRight(m *tui.Model) string {
	thinking := a.Session.Settings().ThinkingLevel
	if thinking == "" {
		thinking = "off"
	}
	thinkingTag := tui.TokenStyle.Render(fmt.Sprintf("thinking:%s", thinking))

	if cu := a.Session.ContextUsage(); cu != nil {
		return tui.TokenStyle.Render(fmt.Sprintf("ctx: %.0f%%", cu.Percent)) + " · " + thinkingTag
	}
	if totalTokens := a.Session.TotalTokens(); totalTokens > 0 {
		return tui.TokenStyle.Render(fmt.Sprintf("tokens: %d", totalTokens)) + " · " + thinkingTag
	}
	return thinkingTag
}
