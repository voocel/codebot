package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/cron"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/plan"
	"github.com/voocel/codebot/internal/plugin"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/tools"
	"github.com/voocel/codebot/internal/ui/commands"
	"github.com/voocel/codebot/internal/ui/imageinput"
	"github.com/voocel/codebot/internal/ui/tui"
)

// App is the TUI adapter. Business logic lives in agent.Session.
type App struct {
	Session   *agent.Session
	Cwd       string
	GitBranch string
	Version   string

	ApprovalEngine *approval.Engine

	// Commands are user-defined slash commands loaded from .md files.
	Commands []config.FileCommand

	// Skills are loaded skill definitions.
	Skills []skill.Spec
	// SkillCatalog provides shared skill invocation behavior across UI and tools.
	SkillCatalog  *skill.Catalog
	PluginCatalog *plugin.Catalog

	// MCPManager manages MCP server connections.
	MCPManager *mcpclient.Manager
	MCPServers map[string]mcpclient.ServerConfig

	// CronStore holds session-scoped cron jobs for /loop command.
	CronStore *cron.Store

	// TaskRuntime provides unified access to all background tasks (shells + agents).
	TaskRuntime *task.Runtime

	// TeamRegistry is the session-wide team registry. nil before any
	// team_create call; the chrome's team chip and any team-aware command
	// degrade to "no team" when nil or HasTeam() returns false.
	TeamRegistry *team.Registry

	// PlanStore persists plans to ~/.codebot/plans/.
	PlanStore *storage.PlanStore

	// SessionStore is used to record plan slug into the session log.
	SessionStore *storage.Store

	// History provides input history for Up/Down navigation.
	History *storage.History

	PlanManager *plan.Manager

	// registry holds all slash commands assembled from built-ins and file-backed sources.
	registry *Registry

	// CommandLoaders allow alternative command sources to be injected at app construction time.
	CommandLoaders []CommandLoader
}

func (a *App) reloadPluginState() error {
	catalog, err := plugin.LoadAll(a.Cwd)
	if err != nil {
		return err
	}
	contrib := catalog.Contributions()
	extraSkillDirs := make([]skill.DirSource, 0, len(contrib.SkillDirs))
	for _, dir := range contrib.SkillDirs {
		extraSkillDirs = append(extraSkillDirs, skill.DirSource{
			Path:   dir.Path,
			Source: dir.Source,
		})
	}
	a.PluginCatalog = catalog
	a.SkillCatalog = skill.NewCatalog(a.Cwd, contrib.SkillSpecs, extraSkillDirs...)
	a.Commands = a.loadPluginCommands()
	a.Skills = a.SkillCatalog.List()
	if a.Session != nil {
		a.Session.SetSkillCatalog(a.SkillCatalog)
	}
	return nil
}

func (a *App) loadPluginCommands() []config.FileCommand {
	if a.PluginCatalog == nil {
		return config.LoadFileCommandsWithSources(a.Cwd)
	}
	contrib := a.PluginCatalog.Contributions()
	extraCommands := make([]config.ExtraCommandSource, 0, len(contrib.CommandDirs))
	for _, dir := range contrib.CommandDirs {
		extraCommands = append(extraCommands, config.ExtraCommandSource{Path: dir, Source: "plugin"})
	}
	return config.LoadFileCommandsWithSources(a.Cwd, extraCommands...)
}

type mcpReloadResult struct {
	Connected int
	Failed    int
	Tools     int
	Errors    []string
}

func (a *App) effectiveMCPServers() map[string]mcpclient.ServerConfig {
	servers := mcpclient.LoadAllMCPServers(a.Cwd)
	if a.PluginCatalog != nil {
		for name, cfg := range a.PluginCatalog.Contributions().MCPServers {
			if servers == nil {
				servers = make(map[string]mcpclient.ServerConfig)
			}
			if _, exists := servers[name]; !exists {
				servers[name] = cfg
			}
		}
	}
	if len(servers) == 0 {
		return nil
	}
	return servers
}

func (a *App) installMCPRefreshHook() {
	if a.Session == nil || a.MCPManager == nil {
		return
	}
	a.Session.SetBeforePrompt(func() {
		mcpTools, ok := a.MCPManager.RefreshIfDirty(context.Background())
		if !ok {
			return
		}
		a.Session.ReplaceMCPTools(mcpTools)
		a.Session.SetMCPInstructions(strings.Join(a.MCPManager.Instructions(), "\n\n"))
	})
}

func (a *App) reloadMCPRuntime() (mcpReloadResult, error) {
	var result mcpReloadResult

	a.MCPServers = a.effectiveMCPServers()
	if a.MCPManager == nil && len(a.MCPServers) == 0 {
		if a.Session != nil {
			a.Session.ReplaceMCPTools(nil)
			a.Session.SetMCPInstructions("")
		}
		return result, nil
	}

	if a.MCPManager == nil {
		a.MCPManager = mcpclient.NewManager()
	}
	a.installMCPRefreshHook()

	reloadCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	errs := a.MCPManager.Reconfigure(reloadCtx, a.MCPServers)

	snapshotCtx, snapshotCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer snapshotCancel()

	tools := a.MCPManager.Tools(snapshotCtx)
	instructions := strings.Join(a.MCPManager.Instructions(), "\n\n")
	statuses := a.MCPManager.Status(snapshotCtx)

	if a.Session != nil {
		a.Session.ReplaceMCPTools(tools)
		a.Session.SetMCPInstructions(instructions)
	}

	result.Tools = len(tools)
	for _, status := range statuses {
		if status.Error != "" {
			result.Failed++
			continue
		}
		result.Connected++
	}
	if len(errs) > 0 {
		result.Errors = make([]string, 0, len(errs))
		for _, err := range errs {
			result.Errors = append(result.Errors, err.Error())
		}
	}
	return result, nil
}

func (a *App) refreshRuntimeAfterPluginReload() (mcpReloadResult, error) {
	mcpResult, err := a.reloadMCPRuntime()
	if err != nil {
		return mcpResult, err
	}
	if a.Session != nil {
		a.Session.Reload()
		a.Skills = a.Session.Skills()
		if found := a.Session.ToolsByName("Skill"); len(found) > 0 {
			if st, ok := found[0].(*tools.SkillTool); ok {
				st.SetCatalog(a.SkillCatalog)
			}
		}
	} else if a.SkillCatalog != nil {
		a.Skills = a.SkillCatalog.List()
	}
	a.rebuildRegistry()
	return mcpResult, nil
}

// Config returns a tui.Config with all hooks wired to this App.
func (a *App) Config() tui.Config {
	if a.PlanManager == nil {
		a.Session.RestoreAllTools()
	}
	plansDir := ""
	if a.PlanStore != nil {
		plansDir = a.PlanStore.Dir()
	}
	return tui.Config{
		Cwd:         a.Cwd,
		PlansDir:    plansDir,
		GitBranch:   a.GitBranch,
		History:     a.History,
		OnKey:       a.onKey(),
		OnPaste:     a.onPaste,
		OnDrop:      a.onDrop,
		OnEvent:     a.planOnEvent,
		StatusRight: a.statusRight,
		StatusMode:  a.statusMode,
		StatusTeam:  a.statusTeam,
		Overlay:     a.overlayState,
		Completions: a.completions,
		OnBtwResult: a.onBtwResult,
	}
}

// onKey returns a hook that intercepts slash commands.
func (a *App) onKey() func(m *tui.Model, msg tea.KeyMsg) (bool, tea.Cmd) {
	return func(m *tui.Model, msg tea.KeyMsg) (bool, tea.Cmd) {
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
			echo := m.Emit(m.RenderPromptOutput(text))
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
		echo := m.Emit(m.RenderPromptOutput(text))
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

// statusTeam returns the active-team chip for the context bar. Empty when no
// team has been created in this session. Format: "△ <name> · <idle>/<total>"
// where idle is the count of teammates parked on their mailbox and total is
// the number of registered teammates (leader excluded). Teammates with
// terminal status (completed, killed) are not counted.
func (a *App) statusTeam(_ *tui.Model) string {
	if a.TeamRegistry == nil || !a.TeamRegistry.HasTeam() {
		return ""
	}
	ctx := a.TeamRegistry.Team()
	if ctx == nil {
		return ""
	}
	total, idle := a.countLiveTeammates()
	if total == 0 {
		// Just the leader so far — show the team name without a count.
		return fmt.Sprintf("△ %s", ctx.Name)
	}
	return fmt.Sprintf("△ %s · %d/%d idle", ctx.Name, idle, total)
}

// countLiveTeammates walks the task runtime once for both numbers so the
// status chip is rendered from one consistent snapshot. Returns (totalLive,
// idleLive) where "live" means Status == Running.
func (a *App) countLiveTeammates() (total, idle int) {
	if a.TaskRuntime == nil {
		return 0, 0
	}
	for _, e := range a.TaskRuntime.List() {
		if e.Type != task.TypeTeammate || e.Status != task.Running {
			continue
		}
		total++
		if e.IsIdle {
			idle++
		}
	}
	return total, idle
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

// ModalOverlay is an optional interface used by overlays that want to opt out
// of the modal default. Overlays that intercept keyboard input (the typical
// case) replace the input area; non-modal overlays — autocomplete-style hint
// panels that coexist with the input — must implement this and return false.
type ModalOverlay interface {
	IsModal() bool
}

// overlayState bridges the registry's interactive command overlay to the TUI.
// Default is modal (ReplacesInput = true) so any overlay capturing keys is
// safe by construction; only overlays that explicitly return IsModal()=false
// keep the input area visible alongside their view.
func (a *App) overlayState(m *tui.Model) *tui.OverlayState {
	ov := a.registry.Overlay()
	if ov == nil || !ov.Active() {
		return nil
	}
	state := &tui.OverlayState{
		HandleKey:     ov.HandleKey,
		View:          ov.View,
		ReplacesInput: true,
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
	if btw, ok := a.registry.Overlay().(*commands.BtwCommand); ok {
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
		return tui.CommandResultMsg{Text: tui.CommandStyle.Render(result), Inline: true}
	}
}
