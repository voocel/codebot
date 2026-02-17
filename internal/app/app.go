package app

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
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
		Placeholder: "Type your message... (Enter to send, Esc to abort, /help for commands)",
		OnKey:       a.onKey(),
		OnEvent:     a.onEvent,
		StatusRight: a.statusRight,
		OnFooter:    a.footer,
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

// onEvent extracts thinking blocks from assistant messages.
func (a *App) onEvent(m *tui.Model, ev agentcore.Event) tea.Cmd {
	if ev.Type != agentcore.EventMessageEnd {
		return nil
	}
	if ev.Message == nil || ev.Message.GetRole() != agentcore.RoleAssistant {
		return nil
	}
	if tc := ev.Message.ThinkingContent(); tc != "" {
		thinking := tui.Block{
			Kind:      tui.BlockThinking,
			Content:   tui.ThinkingStyle.Render("  [thinking] " + tui.TruncateLines(tc, 3)),
			Collapsed: m.ThinkingCollapsed,
			Summary:   "Thinking...",
		}
		if len(m.Blocks) > 0 {
			last := m.Blocks[len(m.Blocks)-1]
			m.Blocks[len(m.Blocks)-1] = thinking
			m.Blocks = append(m.Blocks, last)
		} else {
			m.Blocks = append(m.Blocks, thinking)
		}
		m.RebuildViewport()
	}
	return nil
}

// statusRight displays context usage in the status bar.
func (a *App) statusRight(m *tui.Model) string {
	if cu := m.Agent.ContextUsage(); cu != nil {
		return tui.TokenStyle.Render(fmt.Sprintf("ctx: %.0f%%", cu.Percent))
	}
	usage := m.Agent.TotalUsage()
	if usage.TotalTokens > 0 {
		return tui.TokenStyle.Render(fmt.Sprintf("tokens: %d", usage.TotalTokens))
	}
	return ""
}

// footer renders the bottom information bar.
func (a *App) footer(m *tui.Model) string {
	var parts []string

	if a.GitBranch != "" {
		parts = append(parts, tui.MutedStyle.Render(a.GitBranch))
	}

	parts = append(parts, m.ModelName)

	if cu := m.Agent.ContextUsage(); cu != nil {
		parts = append(parts, fmt.Sprintf("ctx: %.0f%% (%dk)", cu.Percent, cu.Tokens/1000))
	}

	parts = append(parts, fmt.Sprintf("Turn %d", m.TurnCount))

	return strings.Join(parts, "  │  ")
}
