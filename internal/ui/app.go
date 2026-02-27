package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/ui/imageinput"
	"github.com/voocel/codebot/internal/config"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/ui/tui"
)

// App is the TUI adapter. Business logic lives in agent.Session.
type App struct {
	Session   *agent.Session
	Cwd       string
	GitBranch string

	// PolicyProfile controls slash-command risk gating.
	PolicyProfile policy.Profile

	// Templates are user-defined prompt templates loaded from .md files.
	Templates []config.PromptTemplate

	// Skills are loaded skill definitions.
	Skills []config.Skill

	// MCPManager manages MCP server connections.
	MCPManager *mcpclient.Manager

	// PlanStore persists plans to <cwd>/.codebot/plans/.
	PlanStore *storage.PlanStore

	// Plan mode state.
	planState   planState
	planContent string // free-form plan text from LLM
	planTitle   string // short title extracted from plan content
	planChoice  int    // selected option in planReview menu
}

// Config returns a tui.Config with all hooks wired to this App.
func (a *App) Config() tui.Config {
	// Register enter_plan_mode so LLM can proactively enter plan mode.
	a.Session.RestoreAllTools(newEnterPlanModeTool())
	return tui.Config{
		Cwd:         a.Cwd,
		GitBranch:   a.GitBranch,
		OnKey:       a.onKey(),
		OnPaste:     a.onPaste,
		OnDrop:      a.onDrop,
		OnEvent:     a.planOnEvent,
		StatusRight: a.statusRight,
		StatusPlan:  a.planStatus,
	}
}

// onKey returns a hook that intercepts slash commands and plan approval keys.
func (a *App) onKey() func(m *tui.Model, msg tea.KeyMsg) (bool, tea.Cmd) {
	return func(m *tui.Model, msg tea.KeyMsg) (bool, tea.Cmd) {
		// Plan pending approval: up/down to select, enter to confirm.
		if a.planState == planReview && !m.Running {
			switch msg.String() {
			case "up", "k", "left", "h":
				if a.planChoice > 0 {
					a.planChoice--
				}
				return true, nil
			case "down", "j", "right", "l":
				if a.planChoice < 2 {
					a.planChoice++
				}
				return true, nil
			case "enter":
				switch a.planChoice {
				case 0:
					return true, a.executePlan()
				case 1:
					return true, a.editPlan()
				case 2:
					return true, a.cancelPlanMode()
				}
			}
			// Block other input while awaiting approval.
			return true, nil
		}

		if msg.String() != "enter" {
			return false, nil
		}
		text := strings.TrimSpace(m.Input.Value())
		if !strings.HasPrefix(text, "/") {
			return false, nil
		}
		// Slash commands are "/word ..."; file paths like "/Users/..." contain
		// a second slash before any space — skip those.
		cmd := text[1:]
		if i := strings.IndexAny(cmd, " \t"); i > 0 {
			cmd = cmd[:i]
		}
		if strings.Contains(cmd, "/") {
			return false, nil
		}
		m.Input.Reset()
		echo := tea.Println(m.RenderPromptOutput(text))
		m.ShowWelcome = false
		return true, tea.Sequence(echo, a.handleCommand(text))
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

// onPaste returns a tea.Cmd that asynchronously reads clipboard image data.
func (a *App) onPaste(m *tui.Model) tea.Cmd {
	return func() tea.Msg {
		data, err := imageinput.ReadImage()
		if err != nil {
			return tui.PasteErrorMsg{Text: tui.ErrorStyle.Render("clipboard: " + err.Error())}
		}
		if data == nil {
			return tui.PasteTextMsg{} // no image — trigger text paste fallback
		}
		block, err := imageinput.FromBytes(data)
		if err != nil {
			return tui.PasteErrorMsg{Text: tui.ErrorStyle.Render(err.Error())}
		}
		return tui.ImageAttachedMsg{Block: block}
	}
}

// onDrop handles file drag-drop: if the pasted text is an image path, load it.
// Returns nil when the text is not an image path (lets textarea handle it).
func (a *App) onDrop(m *tui.Model, text string) tea.Cmd {
	path := imageinput.ParseDroppedPath(text)
	if path == "" {
		return nil
	}
	return func() tea.Msg {
		block, err := imageinput.LoadFile(path)
		if err != nil {
			return tui.PasteErrorMsg{Text: tui.ErrorStyle.Render(err.Error())}
		}
		return tui.ImageAttachedMsg{Block: block}
	}
}
