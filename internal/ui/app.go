package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/ui/imageinput"
	"github.com/voocel/codebot/internal/ui/tui"
)

// App is the TUI adapter. Business logic lives in agent.Session.
type App struct {
	Session   *agent.Session
	Cwd       string
	GitBranch string

	// PolicyProfile controls slash-command risk gating.
	PolicyProfile policy.Profile

	// Commands are user-defined slash commands loaded from .md files.
	Commands []config.FileCommand

	// Skills are loaded skill definitions.
	Skills []config.Skill

	// MCPManager manages MCP server connections.
	MCPManager *mcpclient.Manager

	// PlanStore persists plans to ~/.codebot/plans/.
	PlanStore *storage.PlanStore

	// History provides input history for Up/Down navigation.
	History *storage.History

	// registry holds all slash commands assembled from built-ins and file-backed sources.
	registry *Registry

	// CommandLoaders allow alternative command sources to be injected at app construction time.
	CommandLoaders []CommandLoader

	// Plan mode state.
	planState     planState
	planContent   string // free-form plan text from LLM
	planTitle     string // short title extracted from plan content
	planChoice    int    // selected option in planReview menu
	planOtherMode bool   // typing custom feedback
	planOtherBuf  string // custom feedback buffer
}

// Config returns a tui.Config with all hooks wired to this App.
func (a *App) Config() tui.Config {
	// Register enter_plan_mode so LLM can proactively enter plan mode.
	a.Session.RestoreAllTools(newEnterPlanModeTool())
	return tui.Config{
		Cwd:         a.Cwd,
		GitBranch:   a.GitBranch,
		History:     a.History,
		OnKey:       a.onKey(),
		OnPaste:     a.onPaste,
		OnDrop:      a.onDrop,
		OnEvent:     a.planOnEvent,
		StatusRight: a.statusRight,
		StatusPlan:  a.planStatus,
		Overlay:     a.overlayState,
		Completions: a.completions,
	}
}

// onKey returns a hook that intercepts slash commands and plan approval keys.
func (a *App) onKey() func(m *tui.Model, msg tea.KeyMsg) (bool, tea.Cmd) {
	return func(m *tui.Model, msg tea.KeyMsg) (bool, tea.Cmd) {
		// Plan pending approval: AskUser-style interaction.
		if a.planState == planReview && !m.Running {
			return a.handlePlanReviewKey(msg)
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

// overlayState bridges the registry's interactive command overlay to the TUI.
func (a *App) overlayState(m *tui.Model) *tui.OverlayState {
	ov := a.registry.Overlay()
	if ov == nil || !ov.Active() {
		return nil
	}
	return &tui.OverlayState{
		HandleKey: ov.HandleKey,
		View:      ov.View,
	}
}

// completions returns matching slash command candidates for the given prefix.
func (a *App) completions(prefix string) []tui.CompletionItem {
	lower := strings.ToLower(prefix)
	var items []tui.CompletionItem
	seen := make(map[string]bool)
	for _, cmd := range a.registry.All() {
		spec := a.registry.EffectiveSpec(cmd)
		if spec.Hidden {
			continue
		}
		if strings.HasPrefix(spec.Name, lower) && !seen[spec.Name] {
			items = append(items, tui.CompletionItem{Name: spec.Name, Description: spec.Description})
			seen[spec.Name] = true
		}
		for _, alias := range spec.Aliases {
			if strings.HasPrefix(alias, lower) && !seen[alias] {
				items = append(items, tui.CompletionItem{Name: alias, Description: spec.Description})
				seen[alias] = true
			}
		}
	}
	return items
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
