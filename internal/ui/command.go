package ui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/apperr"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/tools"
	"github.com/voocel/codebot/internal/ui/commands"
	"github.com/voocel/codebot/internal/ui/tui"
)

func (a *App) handleCommand(input string) tea.Cmd {
	inv, ok := commands.ParseInvocation(input)
	if !ok {
		return nil
	}

	cmd, ok := a.registry.Lookup(inv.Name)
	if !ok {
		return tui.SendCommandResult(tui.CommandStyle.Render(
			fmt.Sprintf("Unknown command: /%s. Type / to browse commands, or /help for the full list.", inv.Name)))
	}

	spec := cmd.Spec()
	if err := validateCommand(context.Background(), a.ApprovalEngine, spec, a.Session.IsRunning()); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Command blocked: " + err.Error()))
	}
	return cmd.Run(inv)
}

func validateCommand(ctx context.Context, engine *approval.Engine, spec commands.Spec, isRunning bool) error {
	if engine == nil {
		if spec.NeedsIdle && isRunning {
			return apperr.NewKind(apperr.KindPermission, "command requires idle agent; press Esc to abort current run")
		}
		return nil
	}
	if err := engine.ApproveCommand(ctx, approval.CommandRequest{
		Name:      spec.Name,
		Category:  approval.NormalizeCommandCategory(spec.Category),
		NeedsIdle: spec.NeedsIdle,
		IsRunning: isRunning,
		Summary:   "/" + spec.Name,
	}); err != nil {
		return apperr.WrapKind(apperr.KindPermission, err.Error(), err)
	}
	return nil
}

// builtinCommands returns all built-in slash commands.
//
// The list doubles as the dependency graph: each constructor's arguments
// declare exactly what the command needs from App. New commands should be
// added by writing a constructor in the commands subpackage and listing it
// here with the specific App fields/callbacks it depends on — never add a
// shared "Deps" interface that aggregates the whole App.
func (a *App) builtinCommands() []commands.Command {
	return []commands.Command{
		commands.Help(a.registry),
		commands.Clear(a.Session, a.resetPlanState),
		commands.Model(a.Session, a.registry, a.Cwd),
		commands.Compact(a.Session),
		&commands.StatusCommand{
			Session:      a.Session,
			Overlay:      a.registry,
			Approval:     a.ApprovalEngine,
			Plugins:      a.PluginCatalog,
			MCP:          a.MCPManager,
			Cron:         a.CronStore,
			PlanPhase:    a.planPhase,
			Cwd:          a.Cwd,
			GitBranch:    a.GitBranch,
			Version:      a.Version,
			SkillCount:   func() int { return len(a.Skills) },
			CommandCount: func() int { return len(a.Commands) },
		},
		commands.Context(a.Session, a.registry),
		commands.New(a.Session, a.resetPlanState),
		commands.Resume(a.Session, a.registry, a.resetPlanState),
		commands.Tasks(a.TaskRuntime, a.registry),
		commands.Btw(a.Session, a.registry),
		commands.Settings(a.Session, a.registry, a.ApprovalEngine, a.Cwd),
		commands.MCP(a.MCPManager),
		&commands.PluginsCommand{
			Catalog:        a.PluginCatalog,
			Session:        a.Session,
			Cwd:            a.Cwd,
			ReloadState:    a.reloadPluginState,
			RefreshRuntime: a.refreshRuntimeForCommands,
		},
		commands.DebugHarness(a.Session, a.registry),
		commands.Copy(a.Session),
		&commands.PlanCommand{
			Phase:  a.planPhase,
			Enter:  a.enterPlanMode,
			Show:   a.showCurrentPlan,
			Cancel: a.cancelPlanMode,
			Open:   a.openCurrentPlan,
		},
		commands.Reload(a.reloadAll),
		commands.Memory(a.Cwd, func() { a.Session.Reload() }),
		commands.Loop(a.CronStore),
		commands.Exit(),
	}
}

// reloadAll triggers a full plugin/skill/MCP reload and returns the counts
// for the /reload command's feedback line.
func (a *App) reloadAll() (commands.ReloadResult, error) {
	if err := a.reloadPluginState(); err != nil {
		return commands.ReloadResult{}, err
	}
	mcpResult, err := a.refreshRuntimeAfterPluginReload()
	if err != nil {
		return commands.ReloadResult{}, err
	}
	return commands.ReloadResult{
		Commands:     len(a.Commands),
		Skills:       len(a.Skills),
		MCPTools:     mcpResult.Tools,
		MCPConnected: mcpResult.Connected,
		MCPFailed:    mcpResult.Failed,
	}, nil
}

// refreshRuntimeForCommands adapts App's MCP reload result into the shape the
// commands subpackage consumes.
func (a *App) refreshRuntimeForCommands() (commands.MCPReloadResult, error) {
	res, err := a.refreshRuntimeAfterPluginReload()
	return commands.MCPReloadResult{
		Connected: res.Connected,
		Failed:    res.Failed,
		Tools:     res.Tools,
		Errors:    res.Errors,
	}, err
}

// sendAsPrompt sends expanded template text as a user message to the agent.
func (a *App) sendAsPrompt(text string) tea.Cmd {
	return func() tea.Msg {
		return tui.PromptMsg{Text: text}
	}
}

func (a *App) executeSkillInvocation(result *skill.InvocationResult) tea.Cmd {
	return func() tea.Msg {
		found := a.Session.ToolsByName("subagent")
		var forkExecutor tools.ForkExecutor
		if len(found) > 0 {
			forkExecutor = found[0].Execute
		}

		execResult, err := tools.ExecuteSkillInvocation(context.Background(), result, a.Session.ApplySkillInvocation, forkExecutor)
		if err != nil {
			if result != nil && result.Mode == skill.ModeFork && len(found) == 0 {
				return tui.CommandResultMsg{Text: tui.ErrorStyle.Render("subagent tool is not available for forked skill execution")}
			}
			return tui.CommandResultMsg{Text: tui.ErrorStyle.Render("Skill execution failed: " + err.Error())}
		}
		if execResult.Forked {
			return tui.CommandResultMsg{
				Text: tui.CommandStyle.Render(tui.FormatSubagentOutput(execResult.ForkOutput)),
			}
		}
		return tui.PromptMsg{Text: execResult.PromptText}
	}
}
