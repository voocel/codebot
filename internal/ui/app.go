package ui

import (
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/cron"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/ui/imageinput"
	"github.com/voocel/codebot/internal/ui/tui"
)

// App is the TUI adapter. Business logic lives in agent.Session.
type App struct {
	Session   *agent.Session
	Cwd       string
	GitBranch string

	ApprovalEngine *approval.Engine

	// Commands are user-defined slash commands loaded from .md files.
	Commands []config.FileCommand

	// Skills are loaded skill definitions.
	Skills []skill.Spec
	// SkillCatalog provides shared skill invocation behavior across UI and tools.
	SkillCatalog *skill.Catalog

	// MCPManager manages MCP server connections.
	MCPManager *mcpclient.Manager

	// CronStore holds session-scoped cron jobs for /loop command.
	CronStore *cron.Store

	// TaskRuntime provides unified access to all background tasks (shells + agents).
	TaskRuntime *agentcore.TaskRuntime

	// PlanStore persists plans to ~/.codebot/plans/.
	PlanStore *storage.PlanStore

	// SessionStore is used to record plan slug into the session log.
	SessionStore *storage.Store

	// History provides input history for Up/Down navigation.
	History *storage.History

	// registry holds all slash commands assembled from built-ins and file-backed sources.
	registry *Registry

	// CommandLoaders allow alternative command sources to be injected at app construction time.
	CommandLoaders []CommandLoader

	// Plan mode state.
	planState     planState
	planSlug      string // plan file identifier, persisted in session log
	planContent   string // free-form plan text from LLM
	planTitle     string // short title extracted from plan content
	planChoice    int    // selected option in planReview menu
	planOtherMode bool   // typing custom feedback
	planOtherBuf  string // custom feedback buffer
}

// Config returns a tui.Config with all hooks wired to this App.
func (a *App) Config() tui.Config {
	// Restore all tools (enter_plan_mode is already registered in buildToolset).
	a.Session.RestoreAllTools()
	return tui.Config{
		Cwd:         a.Cwd,
		GitBranch:   a.GitBranch,
		History:     a.History,
		OnKey:       a.onKey(),
		OnPaste:     a.onPaste,
		OnDrop:      a.onDrop,
		OnEvent:     a.planOnEvent,
		StatusRight: a.statusRight,
		StatusMode:  a.statusMode,
		StatusPlan:  a.planStatus,
		Overlay:     a.overlayState,
		Completions: a.completions,
		OnBtwResult: a.onBtwResult,
	}
}

// onKey returns a hook that intercepts slash commands and plan approval keys.
func (a *App) onKey() func(m *tui.Model, msg tea.KeyMsg) (bool, tea.Cmd) {
	return func(m *tui.Model, msg tea.KeyMsg) (bool, tea.Cmd) {
		// Plan pending approval: AskUser-style interaction.
		if a.planState == planReview && !m.Running {
			return a.handlePlanReviewKey(msg)
		}

		// Shift+Tab: cycle permission mode (only when idle).
		if msg.String() == "shift+tab" && !m.Running {
			return true, a.cycleMode()
		}

		if msg.String() != "enter" {
			return false, nil
		}
		text := strings.TrimSpace(m.Input.Value())

		// Shell escape: "!command" runs directly in the shell.
		if strings.HasPrefix(text, "!") {
			shellCmd := strings.TrimSpace(text[1:])
			if shellCmd == "" {
				return false, nil
			}
			m.Input.Reset()
			echo := tea.Println(m.RenderPromptOutput(text))
			m.ShowWelcome = false
			return true, tea.Sequence(echo, a.execShell(shellCmd))
		}

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

// statusRight displays context usage, token counts, and cost in the status bar.
func (a *App) statusRight(m *tui.Model) string {
	var parts []string
	if cu := a.Session.ContextUsage(); cu != nil {
		parts = append(parts, fmt.Sprintf("ctx: %.0f%%", cu.Percent))
	}
	input, output, cost := a.Session.CostEstimate()
	if input+output > 0 {
		parts = append(parts, fmt.Sprintf("↑%s ↓%s", tui.FormatTokens(input), tui.FormatTokens(output)))
	}
	if cost > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", cost))
	}
	if len(parts) == 0 {
		return ""
	}
	return tui.TokenStyle.Render(strings.Join(parts, " · "))
}

// statusMode returns the mode indicator string for the context bar.
func (a *App) statusMode(_ *tui.Model) string {
	if a.ApprovalEngine == nil {
		return ""
	}
	if a.ApprovalEngine.PlanMode() {
		return "◇ plan mode"
	}
	switch a.ApprovalEngine.Mode() {
	case approval.ModeStrict:
		return "◆ strict"
	case approval.ModeAcceptEdits:
		return "⏵⏵ accept edits"
	case approval.ModeTrust:
		return "⏵⏵ trust"
	default:
		return ""
	}
}

// modeOrder defines the Shift+Tab cycle sequence.
// Plan mode is excluded — it has its own lifecycle via /plan command.
var modeOrder = []approval.Mode{
	approval.ModeStrict,
	approval.ModeBalanced,
	approval.ModeAcceptEdits,
	approval.ModeTrust,
}

// cycleMode advances to the next permission mode.
func (a *App) cycleMode() tea.Cmd {
	if a.ApprovalEngine == nil {
		return nil
	}
	if a.ApprovalEngine.PlanMode() {
		return nil
	}

	current := a.ApprovalEngine.Mode()
	idx := 0
	for i, m := range modeOrder {
		if m == current {
			idx = i
			break
		}
	}
	next := modeOrder[(idx+1)%len(modeOrder)]
	a.ApprovalEngine.SetMode(next)
	return nil
}

// ModalOverlay is an optional interface for overlays that replace the input area.
type ModalOverlay interface {
	IsModal() bool
}

// overlayState bridges the registry's interactive command overlay to the TUI.
func (a *App) overlayState(m *tui.Model) *tui.OverlayState {
	ov := a.registry.Overlay()
	if ov == nil || !ov.Active() {
		return nil
	}
	state := &tui.OverlayState{
		HandleKey: ov.HandleKey,
		View:      ov.View,
	}
	if modal, ok := ov.(ModalOverlay); ok {
		state.ReplacesInput = modal.IsModal()
	}
	return state
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

// onBtwResult forwards BtwResultMsg to the active BtwCommand overlay.
func (a *App) onBtwResult(msg tui.BtwResultMsg) {
	ov := a.registry.Overlay()
	if btw, ok := ov.(*BtwCommand); ok {
		btw.SetResult(msg)
	}
}

// execShell runs a shell command and displays the output in scrollback.
func (a *App) execShell(cmd string) tea.Cmd {
	return func() tea.Msg {
		c := exec.Command("sh", "-c", cmd)
		c.Dir = a.Cwd
		out, err := c.CombinedOutput()
		result := strings.TrimRight(string(out), "\n")
		if err != nil && result != "" {
			result += "\n" + err.Error()
		} else if err != nil {
			result = err.Error()
		}
		return tui.CommandResultMsg{Text: tui.CommandStyle.Render(result)}
	}
}
