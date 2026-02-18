package app

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/tui"
)

// App is the TUI adapter. Business logic lives in agent.Session.
type App struct {
	Session   agent.Session
	Cwd       string
	GitBranch string

	// PolicyProfile controls slash-command risk gating.
	PolicyProfile policy.Profile
}

// Config returns a tui.Config with all hooks wired to this App.
func (a *App) Config() tui.Config {
	return tui.Config{
		Placeholder: "输入消息（Enter 发送，↑/↓ 浏览历史，Esc 中断，/help 查看命令）",
		Cwd:         a.Cwd,
		GitBranch:   a.GitBranch,
		OnKey:       a.onKey(),
		StatusRight: a.statusRight,
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
		return true, a.handleCommand(text, m.Agent, m.ModelName)
	}
}

// statusRight displays context usage in the status bar.
func (a *App) statusRight(m *tui.Model) string {
	thinking := a.Session.Settings().ThinkingLevel
	if thinking == "" {
		thinking = "off"
	}
	thinkingTag := tui.TokenStyle.Render(fmt.Sprintf("thinking:%s", thinking))

	if cu := m.Agent.ContextUsage(); cu != nil {
		return tui.TokenStyle.Render(fmt.Sprintf("ctx: %.0f%%", cu.Percent)) + "  " + thinkingTag
	}
	usage := m.Agent.TotalUsage()
	if usage.TotalTokens > 0 {
		return tui.TokenStyle.Render(fmt.Sprintf("tokens: %d", usage.TotalTokens)) + "  " + thinkingTag
	}
	return thinkingTag
}
