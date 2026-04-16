package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/apperr"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/cron"
	"github.com/voocel/codebot/internal/plugin"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/tools"
	"github.com/voocel/codebot/internal/ui/tui"
)

func (a *App) handleCommand(input string) tea.Cmd {
	inv, ok := parseCommandInvocation(input)
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
	return cmd.Run(&CommandContext{App: a}, inv)
}

func validateCommand(ctx context.Context, engine *approval.Engine, spec CommandSpec, isRunning bool) error {
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

func validatePluginMutation(isRunning bool) error {
	if isRunning {
		return apperr.NewKind(apperr.KindPermission, "agent is running; press Esc to abort first")
	}
	return nil
}

func parseCommandInvocation(input string) (CommandInvocation, bool) {
	input = strings.TrimSpace(input)
	if input == "" || !strings.HasPrefix(input, "/") {
		return CommandInvocation{}, false
	}

	body := strings.TrimSpace(strings.TrimPrefix(input, "/"))
	if body == "" {
		return CommandInvocation{}, false
	}

	name := body
	rawArgs := ""
	if idx := strings.IndexAny(body, " \t"); idx >= 0 {
		name = body[:idx]
		rawArgs = strings.TrimSpace(body[idx+1:])
	}

	return CommandInvocation{
		Input:   input,
		Name:    strings.ToLower(name),
		RawArgs: rawArgs,
		Args:    ParseArgs(rawArgs),
	}, true
}

// builtinCommands returns all built-in slash commands.
func (a *App) builtinCommands() []Command {
	return []Command{
		NewHelpCommand(a),
		NewSimple(CommandSpec{
			Name: "clear", Usage: "/clear", Description: "Clear current context (memory only)",
			Category: "session", NeedsIdle: true, Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			ctx.App.Session.ClearConversation()
			ctx.App.resetPlanState()
			return func() tea.Msg {
				return tui.CommandResultMsg{
					Text:  tui.SystemMsgStyle.Render("Current context cleared (session history is kept)."),
					Clear: true,
				}
			}
		}),
		NewModelCommand(a),
		NewSimple(CommandSpec{
			Name: "compact", Usage: "/compact", Description: "Compact conversation context",
			Category: "session", NeedsIdle: true, Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return ctx.App.cmdCompact()
		}),
		NewSimple(CommandSpec{
			Name: "session", Usage: "/session", Description: "Show current session info",
			Category: "info", Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return ctx.App.cmdSession()
		}),
		NewContextCommand(a),
		NewSimple(CommandSpec{
			Name: "new", Usage: "/new", Description: "Start new session",
			Category: "session", NeedsIdle: true, Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return ctx.App.cmdNew()
		}),
		NewResumeCommand(a),
		NewTasksCommand(a),
		NewBtwCommand(a),
		NewSettingsCommand(a),
		NewSimple(CommandSpec{
			Name: "mcp", Usage: "/mcp", Description: "Show MCP server status",
			Category: "info", Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return ctx.App.cmdMCP()
		}),
		NewSimple(CommandSpec{
			Name: "plugins", Usage: "/plugins [list|show|validate|create|install|remove|enable|disable|trust] ...", Description: "Inspect or manage plugins",
			Category: "info", Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, inv CommandInvocation) tea.Cmd {
			return ctx.App.cmdPlugins(inv.Args)
		}),
		NewDebugHarnessCommand(a),
		NewSimple(CommandSpec{
			Name: "copy", Usage: "/copy", Description: "Copy last response to clipboard",
			Category: "info", Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return ctx.App.cmdCopy()
		}),
		NewSimple(CommandSpec{
			Name: "plan", Usage: "/plan [open|cancel|<task>]", Description: "Enter plan mode or manage plans",
			Category: "plan", NeedsIdle: true, Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, inv CommandInvocation) tea.Cmd {
			return ctx.App.cmdPlan(inv.Args)
		}),
		NewSimple(CommandSpec{
			Name: "reload", Usage: "/reload", Description: "Reload skills, prompts, and commands",
			Category: "session", NeedsIdle: true, Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return ctx.App.cmdReload()
		}),
		NewSimple(CommandSpec{
			Name: "memory", Usage: "/memory", Description: "Show or edit auto memory",
			Category: "info", Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, inv CommandInvocation) tea.Cmd {
			return ctx.App.cmdMemory(inv.Args)
		}),
		NewSimple(CommandSpec{
			Name: "loop", Usage: "/loop <interval|cron> <prompt>",
			Description: "Schedule recurring prompts",
			Category:    "session", Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, inv CommandInvocation) tea.Cmd {
			return ctx.App.cmdLoop(inv.RawArgs)
		}),
		NewSimple(CommandSpec{
			Name: "exit", Aliases: []string{"quit", "q"},
			Usage: "/exit", Description: "Quit",
			Category: "exit", Kind: CommandKindBuiltin,
		}, func(_ *CommandContext, _ CommandInvocation) tea.Cmd {
			return func() tea.Msg { return tui.CommandResultMsg{Quit: true} }
		}),
	}
}

func (a *App) cmdModel(args []string) tea.Cmd {
	currentModel := a.Session.ModelName()
	if len(args) == 0 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "Current model: %s\n", currentModel)
		if reg := a.Session.Registry(); reg != nil {
			if models := reg.List(""); len(models) > 0 {
				sb.WriteString("\nAvailable models:\n")
				for _, m := range models {
					marker := "  "
					if provider.SameModelID(m.ID, currentModel) {
						marker = "* "
					}
					ctx := tui.FormatTokens(m.ContextWindow)
					reasoning := ""
					if m.Reasoning {
						reasoning = "  reasoning"
					}
					fmt.Fprintf(&sb, "%s%-12s/%-30s %-20s %6s%s\n",
						marker, m.Provider, m.ID, m.Name, ctx, reasoning)
				}
			}
		}
		sb.WriteString("\nUsage: /model <name>")
		return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
	}

	pattern := strings.Join(args, " ")
	resolved, err := a.Session.ResolveAndSetModel(pattern)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			fmt.Sprintf("Failed to switch model: %v", err)))
	}

	return func() tea.Msg {
		return tui.CommandResultMsg{
			Text:             tui.SystemMsgStyle.Render(fmt.Sprintf("Switched to model: %s", resolved)),
			NewProvider:      a.Session.Provider(),
			NewModel:         a.Session.ModelName(),
			NewContextWindow: a.Session.Settings().ContextWindow,
		}
	}
}

func (a *App) cmdCompact() tea.Cmd {
	run := func() tea.Msg {
		result, err := a.Session.Compact()
		if err != nil {
			return tui.CommandResultMsg{
				Text: tui.ErrorStyle.Render("Compaction failed: " + err.Error()),
			}
		}
		if !result.Changed {
			return tui.CommandResultMsg{
				Text: tui.MutedStyle.Render("Context unchanged; nothing worth compacting yet."),
			}
		}
		return tui.CommandResultMsg{
			Text: tui.SystemMsgStyle.Render(fmt.Sprintf(
				"Context compacted: %s -> %s.",
				tui.FormatTokens(result.TokensBefore),
				tui.FormatTokens(result.TokensAfter),
			)),
		}
	}

	return tea.Sequence(
		tui.SendCommandResult(tui.MutedStyle.Render("Compacting context...")),
		run,
	)
}

func (a *App) cmdSession() tea.Cmd {
	info, err := a.Session.CurrentSessionInfo()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to load session info: " + err.Error()))
	}
	name := info.ID
	if info.Name != "" {
		name = info.Name + " (" + info.ID + ")"
	}
	text := fmt.Sprintf("Session: %s\nPath: %s\nCwd: %s\nCreated: %s",
		name, info.Path, info.Cwd, info.Created.Format("2006-01-02 15:04:05"))

	// Append cost estimate if tokens have been used.
	inTok, outTok, cost := a.Session.CostEstimate()
	if inTok+outTok > 0 {
		text += fmt.Sprintf("\nCost: ~$%.4f (%s in / %s out)",
			cost, tui.FormatTokens(inTok), tui.FormatTokens(outTok))
	}

	return tui.SendCommandResult(tui.CommandStyle.Render(text))
}

func (a *App) cmdNew() tea.Cmd {
	if err := a.Session.Reset(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to create session: " + err.Error()))
	}
	a.resetPlanState()
	return func() tea.Msg {
		return tui.CommandResultMsg{
			Text:  tui.SystemMsgStyle.Render("New session started."),
			Clear: true,
		}
	}
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func (a *App) cmdCopy() tea.Cmd {
	text := a.Session.LastAssistantText()
	if text == "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render("No assistant response to copy."))
	}
	if err := clipboard.WriteAll(text); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Clipboard write failed: " + err.Error()))
	}
	n := len([]rune(text))
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(fmt.Sprintf("Copied %d characters to clipboard.", n)))
}

func (a *App) cmdMCP() tea.Cmd {
	if a.MCPManager == nil {
		return tui.SendCommandResult(tui.CommandStyle.Render("No MCP servers configured."))
	}

	mgr := a.MCPManager
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		servers := mgr.Status(ctx)
		if len(servers) == 0 {
			return tui.CommandResultMsg{Text: tui.CommandStyle.Render("No MCP servers found.")}
		}

		var connected, failed int
		totalTools := 0
		var sb strings.Builder
		sb.WriteString("\nMCP Servers:\n")
		for _, s := range servers {
			if s.Error != "" {
				fmt.Fprintf(&sb, "%s %-20s %s\n",
					tui.ErrorStyle.Render("●"), s.Name, tui.ErrorStyle.Render(s.Error))
				failed++
			} else if s.ListError != "" {
				fmt.Fprintf(&sb, "%s %-20s %s\n",
					tui.ErrorStyle.Render("●"), s.Name, tui.ErrorStyle.Render("tools/list: "+s.ListError))
				connected++
			} else {
				fmt.Fprintf(&sb, "%s %-20s %d tools\n",
					tui.DiffAddStyle.Render("●"), s.Name, s.ToolCount)
				totalTools += s.ToolCount
				connected++
			}
		}
		fmt.Fprintf(&sb, "\nTotal: %d connected, %d failed, %d tools", connected, failed, totalTools)
		return tui.CommandResultMsg{Text: tui.CommandStyle.Render(sb.String())}
	}
}

func (a *App) cmdPlugins(args []string) tea.Cmd {
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "list":
			return a.cmdPluginsList()
		case "show":
			return a.cmdPluginsShow(args[1:])
		case "validate":
			return a.cmdPluginsValidate(args[1:])
		case "create":
			return a.cmdPluginsCreate(args[1:])
		case "install":
			return a.cmdPluginsInstall(args[1:])
		case "remove":
			return a.cmdPluginsRemove(args[1:])
		case "enable", "disable", "trust":
			return a.cmdPluginsMutate(args)
		}
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins [list|show|validate|create|install|remove|enable|disable|trust] ..."))
	}
	return a.cmdPluginsList()
}

func (a *App) cmdPluginsList() tea.Cmd {
	if a.PluginCatalog == nil {
		return tui.SendCommandResult(tui.CommandStyle.Render("Plugins\n\nNo plugin catalog loaded."))
	}

	plugins := a.PluginCatalog.Plugins()
	if len(plugins) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("Plugins\n\nNo plugins loaded."))
	}

	var sb strings.Builder
	sb.WriteString("Plugins\n\n")
	for _, p := range plugins {
		status := "enabled"
		if !p.State.Enabled {
			status = "disabled"
		}
		fmt.Fprintf(&sb, "%s (%s)\n", p.Manifest.Name, p.Manifest.ID)
		fmt.Fprintf(&sb, "  version: %s\n", p.Manifest.Version)
		fmt.Fprintf(&sb, "  scope:   %s\n", p.Scope)
		fmt.Fprintf(&sb, "  status:  %s\n", status)
		fmt.Fprintf(&sb, "  trust:   %s\n", p.State.Trust)
		fmt.Fprintf(&sb, "  skills:  %d\n", p.SkillCount())
		fmt.Fprintf(&sb, "  commands:%d\n", p.CommandCount())
		fmt.Fprintf(&sb, "  mcp:     %d\n", p.MCPCount())
		if desc := strings.TrimSpace(p.Manifest.Description); desc != "" {
			fmt.Fprintf(&sb, "  about:   %s\n", desc)
		}
		sb.WriteString("\n")
	}

	return tui.SendCommandResult(tui.CommandStyle.Render(strings.TrimRight(sb.String(), "\n")))
}

func (a *App) cmdPluginsShow(args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins show <plugin-id>"))
	}
	loaded, ok := a.findPlugin(args[0])
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin: " + strings.TrimSpace(args[0])))
	}

	status := "enabled"
	if !loaded.State.Enabled {
		status = "disabled"
	}
	var sb strings.Builder
	sb.WriteString("Plugin\n\n")
	fmt.Fprintf(&sb, "name:     %s\n", loaded.Manifest.Name)
	fmt.Fprintf(&sb, "id:       %s\n", loaded.Manifest.ID)
	fmt.Fprintf(&sb, "version:  %s\n", loaded.Manifest.Version)
	fmt.Fprintf(&sb, "scope:    %s\n", loaded.Scope)
	fmt.Fprintf(&sb, "status:   %s\n", status)
	fmt.Fprintf(&sb, "trust:    %s\n", loaded.State.Trust)
	if loaded.RootDir != "" {
		fmt.Fprintf(&sb, "root:     %s\n", loaded.RootDir)
	}
	fmt.Fprintf(&sb, "skills:   %d\n", loaded.SkillCount())
	fmt.Fprintf(&sb, "commands: %d\n", loaded.CommandCount())
	fmt.Fprintf(&sb, "mcp:      %d\n", loaded.MCPCount())
	if desc := strings.TrimSpace(loaded.Manifest.Description); desc != "" {
		fmt.Fprintf(&sb, "about:    %s\n", desc)
	}
	if !loaded.IsTrusted() {
		sb.WriteString("policy:   untrusted plugins cannot contribute MCP servers and their skills run without privileged fields\n")
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
}

func (a *App) cmdPluginsMutate(args []string) tea.Cmd {
	if len(args) < 2 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins [enable|disable|trust] ..."))
	}
	if a.PluginCatalog == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin catalog not loaded."))
	}
	if a.Session != nil {
		if err := validatePluginMutation(a.Session.IsRunning()); err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
		}
	}

	action := strings.ToLower(strings.TrimSpace(args[0]))
	id := strings.TrimSpace(args[1])
	enable := false
	switch action {
	case "enable":
		enable = true
	case "disable":
		enable = false
	case "trust":
		return a.cmdPluginsTrust(id, args[2:])
	default:
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins [enable|disable|trust] ..."))
	}

	loaded, ok := a.findPlugin(id)
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin: " + id))
	}
	if err := plugin.SetEnabled(a.Cwd, loaded, enable); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin update failed: " + err.Error()))
	}
	if err := a.reloadPluginState(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin reload failed: " + err.Error()))
	}
	mcpResult, err := a.refreshRuntimeAfterPluginReload()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Runtime reload failed: " + err.Error()))
	}

	status := "disabled"
	if enable {
		status = "enabled"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin %s %s.", loaded.Manifest.ID, status)
	if loaded.MCPCount() > 0 || len(mcpResult.Errors) > 0 {
		fmt.Fprintf(&sb, " MCP runtime reloaded: %d connected, %d failed, %d tools.", mcpResult.Connected, mcpResult.Failed, mcpResult.Tools)
	}
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(sb.String()))
}

func (a *App) cmdPluginsTrust(id string, args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins trust <plugin-id> <trusted|untrusted>"))
	}
	if a.Session != nil {
		if err := validatePluginMutation(a.Session.IsRunning()); err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
		}
	}
	loaded, ok := a.findPlugin(id)
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin: " + id))
	}
	trust := strings.ToLower(strings.TrimSpace(args[0]))
	switch trust {
	case plugin.TrustTrusted, plugin.TrustUntrusted:
	default:
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins trust <plugin-id> <trusted|untrusted>"))
	}
	if err := plugin.SetTrust(a.Cwd, loaded, trust); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin trust update failed: " + err.Error()))
	}
	if err := a.reloadPluginState(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin reload failed: " + err.Error()))
	}
	mcpResult, err := a.refreshRuntimeAfterPluginReload()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Runtime reload failed: " + err.Error()))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin %s trust set to %s.", loaded.Manifest.ID, trust)
	if trust == plugin.TrustUntrusted {
		sb.WriteString(" MCP contributions are disabled and skill privileged fields are stripped.")
	} else if loaded.MCPCount() > 0 || len(mcpResult.Errors) > 0 {
		fmt.Fprintf(&sb, " MCP runtime reloaded: %d connected, %d failed, %d tools.", mcpResult.Connected, mcpResult.Failed, mcpResult.Tools)
	}
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(sb.String()))
}

func (a *App) cmdPluginsCreate(args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins create <plugin-id> [project|user|--project|--user]"))
	}
	if a.Session != nil {
		if err := validatePluginMutation(a.Session.IsRunning()); err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
		}
	}

	scope := plugin.ScopeProject
	if len(args) > 1 {
		switch strings.ToLower(strings.TrimSpace(args[1])) {
		case "project", "--project":
			scope = plugin.ScopeProject
		case "user", "--user":
			scope = plugin.ScopeUser
		default:
			return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins create <plugin-id> [project|user|--project|--user]"))
		}
	}

	created, err := plugin.Scaffold(plugin.ScaffoldInput{
		Cwd:   a.Cwd,
		ID:    args[0],
		Scope: scope,
	})
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin scaffold failed: " + err.Error()))
	}
	if err := a.reloadPluginState(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin reload failed: " + err.Error()))
	}
	if _, err := a.refreshRuntimeAfterPluginReload(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Runtime reload failed: " + err.Error()))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin scaffold created: %s (%s).\n", created.ID, created.Scope)
	fmt.Fprintf(&sb, "root: %s\n", created.RootDir)
	fmt.Fprintf(&sb, "manifest: %s\n", created.ManifestPath)
	sb.WriteString("next: edit plugin.json, add files under skills/ or commands/, then run /reload.")
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(sb.String()))
}

func (a *App) cmdPluginsValidate(args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins validate <plugin-id|path>"))
	}

	target := strings.TrimSpace(args[0])
	var report *plugin.ValidationReport
	var err error

	if _, statErr := os.Stat(target); statErr == nil {
		report, err = plugin.ValidatePath(target, "external")
	} else {
		loaded, ok := a.findPlugin(target)
		if !ok {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin or path: " + target))
		}
		report, err = plugin.ValidateLoaded(loaded)
	}
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin validation failed: " + err.Error()))
	}

	var sb strings.Builder
	sb.WriteString("Plugin Validation\n\n")
	fmt.Fprintf(&sb, "id:        %s\n", report.Manifest.ID)
	fmt.Fprintf(&sb, "name:      %s\n", report.Manifest.Name)
	fmt.Fprintf(&sb, "version:   %s\n", report.Manifest.Version)
	fmt.Fprintf(&sb, "scope:     %s\n", report.Scope)
	fmt.Fprintf(&sb, "root:      %s\n", report.RootDir)
	if report.State != nil {
		status := "enabled"
		if !report.State.Enabled {
			status = "disabled"
		}
		fmt.Fprintf(&sb, "status:    %s\n", status)
		fmt.Fprintf(&sb, "trust:     %s\n", report.State.Trust)
	}
	fmt.Fprintf(&sb, "skills:    %d\n", report.SkillCount)
	fmt.Fprintf(&sb, "commands:  %d\n", report.CommandCount)
	fmt.Fprintf(&sb, "mcp:       %d\n", report.MCPCount)
	fmt.Fprintf(&sb, "summary:   %s\n", report.Summary())
	if len(report.Warnings) > 0 {
		sb.WriteString("\nWarnings:\n")
		for _, warning := range report.Warnings {
			sb.WriteString("  - " + warning + "\n")
		}
	}
	if len(report.Errors) > 0 {
		sb.WriteString("\nErrors:\n")
		for _, issue := range report.Errors {
			sb.WriteString("  - " + issue + "\n")
		}
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(strings.TrimRight(sb.String(), "\n")))
}

func (a *App) cmdPluginsInstall(args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins install <path> [project|user|--project|--user]"))
	}
	if a.Session != nil {
		if err := validatePluginMutation(a.Session.IsRunning()); err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
		}
	}

	scope := plugin.ScopeProject
	if len(args) > 1 {
		switch strings.ToLower(strings.TrimSpace(args[1])) {
		case "project", "--project":
			scope = plugin.ScopeProject
		case "user", "--user":
			scope = plugin.ScopeUser
		default:
			return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins install <path> [project|user|--project|--user]"))
		}
	}

	installed, err := plugin.InstallLocal(plugin.InstallInput{
		Cwd:        a.Cwd,
		SourcePath: args[0],
		Scope:      scope,
	})
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin install failed: " + err.Error()))
	}
	if err := a.reloadPluginState(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin reload failed: " + err.Error()))
	}
	if _, err := a.refreshRuntimeAfterPluginReload(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Runtime reload failed: " + err.Error()))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin installed: %s (%s).\n", installed.ID, installed.Scope)
	fmt.Fprintf(&sb, "root: %s\n", installed.RootDir)
	fmt.Fprintf(&sb, "manifest: %s", installed.ManifestPath)
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(sb.String()))
}

func (a *App) cmdPluginsRemove(args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins remove <plugin-id>"))
	}
	if a.Session != nil {
		if err := validatePluginMutation(a.Session.IsRunning()); err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
		}
	}

	loaded, ok := a.findPlugin(args[0])
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin: " + strings.TrimSpace(args[0])))
	}
	if loaded.Scope == "builtin" {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Builtin plugins cannot be removed."))
	}
	if err := plugin.Remove(a.Cwd, loaded); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin remove failed: " + err.Error()))
	}
	if err := a.reloadPluginState(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin reload failed: " + err.Error()))
	}
	if _, err := a.refreshRuntimeAfterPluginReload(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Runtime reload failed: " + err.Error()))
	}

	return tui.SendCommandResult(tui.SystemMsgStyle.Render("Plugin " + loaded.Manifest.ID + " removed."))
}

func (a *App) findPlugin(id string) (plugin.Loaded, bool) {
	if a.PluginCatalog == nil {
		return plugin.Loaded{}, false
	}
	id = strings.ToLower(strings.TrimSpace(id))
	for _, loaded := range a.PluginCatalog.Plugins() {
		if strings.ToLower(loaded.Manifest.ID) == id {
			return loaded, true
		}
	}
	return plugin.Loaded{}, false
}

func (a *App) cmdReload() tea.Cmd {
	if err := a.reloadPluginState(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Reload failed: " + err.Error()))
	}
	mcpResult, err := a.refreshRuntimeAfterPluginReload()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Runtime reload failed: " + err.Error()))
	}
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(
		fmt.Sprintf("Reloaded: %d commands, %d skills, %d MCP tools (%d connected, %d failed).", len(a.Commands), len(a.Skills), mcpResult.Tools, mcpResult.Connected, mcpResult.Failed)))
}

func (a *App) cmdMemory(args []string) tea.Cmd {
	memDir := config.MemoryDir(a.Cwd)
	memPath := config.MemoryFilePath(a.Cwd)

	if len(args) > 0 && args[0] == "edit" {
		// Ensure file exists so editors that reject missing paths still work.
		config.EnsureMemoryDir(a.Cwd)
		if _, err := os.Stat(memPath); os.IsNotExist(err) {
			_ = os.WriteFile(memPath, []byte("# Project Memory\n"), 0o644)
		}
		return a.openEditor(memPath, "Memory reloaded.")
	}

	// Show memory status.
	var sb strings.Builder
	sb.WriteString("Auto Memory\n\n")
	fmt.Fprintf(&sb, "Directory:  %s\n", memDir)
	fmt.Fprintf(&sb, "Index:      %s\n", memPath)

	entries, _ := os.ReadDir(memDir)
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			files = append(files, e.Name())
		}
	}
	if len(files) == 0 {
		sb.WriteString("\nNo memory files yet. The LLM will create MEMORY.md as it learns.\n")
	} else {
		fmt.Fprintf(&sb, "\nFiles (%d):\n", len(files))
		for _, f := range files {
			sb.WriteString("  " + f + "\n")
		}
	}
	sb.WriteString("\nUse /memory edit to open MEMORY.md in your editor.")
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
}

func (a *App) openEditor(path, successText string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return tui.CommandResultMsg{Text: tui.ErrorStyle.Render("editor: " + err.Error())}
		}
		a.Session.Reload()
		return tui.CommandResultMsg{Text: tui.SystemMsgStyle.Render(successText)}
	})
}

func (a *App) cmdLoop(rawArgs string) tea.Cmd {
	if a.CronStore == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Cron scheduling is not available."))
	}

	args := strings.TrimSpace(rawArgs)
	if args == "" {
		return tui.SendCommandResult(tui.CommandStyle.Render(
			"Usage:\n  /loop <interval|cron> <prompt>  — create a recurring job\n  /loop list                       — list all jobs\n  /loop stop <id|all>              — stop a job or all jobs\n\nExamples:\n  /loop 5m run tests\n  /loop \"*/10 * * * *\" check build status"))
	}

	// /loop list
	if args == "list" {
		return a.cmdLoopList()
	}

	// /loop stop <id|all>
	if strings.HasPrefix(args, "stop ") || args == "stop" {
		target := strings.TrimSpace(strings.TrimPrefix(args, "stop"))
		return a.cmdLoopStop(target)
	}

	// Parse: schedule + prompt.
	schedule, prompt := parseLoopArgs(args)
	if schedule == "" || prompt == "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Invalid syntax. Usage: /loop <interval|cron> <prompt>"))
	}

	job, err := a.CronStore.Create(schedule, prompt, true, true)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(fmt.Sprintf("Failed to create job: %s", err)))
	}

	desc := cron.HumanSchedule(schedule)
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(
		fmt.Sprintf("Scheduled job %s (%s): %q\nNext fire: %s",
			job.ID, desc, prompt, job.NextFire().Format("15:04:05"))))
}

func (a *App) cmdLoopList() tea.Cmd {
	jobs := a.CronStore.List()
	if len(jobs) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("No scheduled jobs."))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Scheduled jobs (%d):\n", len(jobs))
	for _, j := range jobs {
		mode := "recurring"
		if !j.Recurring {
			mode = "one-shot"
		}
		desc := cron.HumanSchedule(j.Schedule)
		fmt.Fprintf(&sb, "  %s  %-20s [%s]  %q  (next: %s)\n",
			j.ID, desc, mode, j.Prompt, j.NextFire().Format("15:04:05"))
	}
	sb.WriteString("\nUse /loop stop <id> to remove a job.")
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
}

func (a *App) cmdLoopStop(target string) tea.Cmd {
	if target == "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /loop stop <id|all>"))
	}
	if target == "all" {
		n := a.CronStore.DeleteAll()
		return tui.SendCommandResult(tui.SystemMsgStyle.Render(fmt.Sprintf("Stopped all %d jobs.", n)))
	}
	if err := a.CronStore.Delete(target); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(err.Error()))
	}
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(fmt.Sprintf("Job %s stopped.", target)))
}

// parseLoopArgs extracts schedule and prompt from /loop args.
// Supports: "5m run tests", "\"*/5 * * * *\" check build"
func parseLoopArgs(args string) (schedule, prompt string) {
	// Quoted schedule: "/loop "*/5 * * * *" check build"
	if strings.HasPrefix(args, "\"") || strings.HasPrefix(args, "'") {
		quote := args[0]
		end := strings.IndexByte(args[1:], quote)
		if end >= 0 {
			schedule = args[1 : end+1]
			prompt = strings.TrimSpace(args[end+2:])
			return schedule, prompt
		}
	}

	// Try to detect cron expression (5 fields before the prompt).
	// A cron field can only contain: digits, *, /, -, comma.
	fields := strings.Fields(args)
	if len(fields) >= 6 && looksLikeCronFields(fields[:5]) {
		return strings.Join(fields[:5], " "), strings.Join(fields[5:], " ")
	}

	// Simple interval: first token is schedule, rest is prompt.
	if len(fields) >= 2 {
		return fields[0], strings.Join(fields[1:], " ")
	}

	return "", ""
}

func looksLikeCronFields(fields []string) bool {
	for _, f := range fields {
		for _, ch := range f {
			if !((ch >= '0' && ch <= '9') || ch == '*' || ch == '/' || ch == '-' || ch == ',') {
				return false
			}
		}
	}
	return true
}

func formatBool(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func formatReminderCounts(counts map[agent.RuntimeReminderKind]int) string {
	order := []agent.RuntimeReminderKind{
		agent.ReminderRepeatToolCall,
		agent.ReminderPostStopValidation,
		agent.ReminderTaskManagement,
	}
	parts := make([]string, 0, len(counts))
	for _, kind := range order {
		if count := counts[kind]; count > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", kind, count))
		}
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

func formatCompactionCounts(counts map[agent.CompactionKind]int) string {
	order := []agent.CompactionKind{
		agent.CompactionKindMicro,
		agent.CompactionKindTrim,
		agent.CompactionKindPrune,
		agent.CompactionKindFull,
	}
	parts := make([]string, 0, len(counts))
	for _, kind := range order {
		if count := counts[kind]; count > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", kind, count))
		}
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

func formatCompactionSavings(savings map[agent.CompactionKind]int) string {
	order := []agent.CompactionKind{
		agent.CompactionKindMicro,
		agent.CompactionKindTrim,
		agent.CompactionKindPrune,
		agent.CompactionKindFull,
	}
	parts := make([]string, 0, len(savings))
	for _, kind := range order {
		if saved := savings[kind]; saved > 0 {
			parts = append(parts, fmt.Sprintf("%s=%s", kind, tui.FormatTokens(saved)))
		}
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

func formatErrorCounts(counts map[apperr.Kind]int) string {
	order := []apperr.Kind{
		apperr.KindCanceled,
		apperr.KindConfig,
		apperr.KindPermission,
		apperr.KindProvider,
		apperr.KindSession,
		apperr.KindToolInput,
		apperr.KindToolExec,
		apperr.KindUnknown,
	}
	parts := make([]string, 0, len(counts))
	for _, kind := range order {
		if count := counts[kind]; count > 0 {
			label := string(kind)
			if kind == apperr.KindUnknown {
				label = "unknown"
			}
			parts = append(parts, fmt.Sprintf("%s=%d", label, count))
		}
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

func formatRecentToolCalls(calls []agent.ToolCallSnapshot) []string {
	if len(calls) == 0 {
		return nil
	}

	lines := make([]string, 0, len(calls))
	for _, call := range calls {
		status := "ok"
		if !call.Success {
			status = "error"
		}
		argsHash := call.ArgsHash
		if argsHash == "" {
			argsHash = "-"
		}
		lines = append(lines, fmt.Sprintf("%s  %-12s %-5s args:%s",
			call.Timestamp.Format("15:04:05"),
			call.Tool,
			status,
			argsHash,
		))
	}
	return lines
}

func formatRecentErrors(errors []agent.ErrorSnapshot) []string {
	if len(errors) == 0 {
		return nil
	}

	lines := make([]string, 0, len(errors))
	for _, snapshot := range errors {
		kind := string(snapshot.Kind)
		if snapshot.Kind == apperr.KindUnknown {
			kind = "unknown"
		}
		line := fmt.Sprintf("%s  %-12s %s",
			snapshot.Timestamp.Format("15:04:05"),
			kind,
			snapshot.Message,
		)
		if snapshot.Detail != "" {
			line += " | " + snapshot.Detail
		}
		lines = append(lines, line)
	}
	return lines
}

func formatLastReminder(snapshot agent.ReminderSnapshot, ok bool) string {
	if !ok {
		return "(none)"
	}
	return fmt.Sprintf("%s via %s at %s", snapshot.Kind, snapshot.Mode, snapshot.Timestamp.Format("15:04:05"))
}

func formatLastCompaction(snapshot agent.CompactionSnapshot, ok bool) string {
	if !ok {
		return "(none)"
	}
	status := "no-op"
	if snapshot.Changed {
		status = fmt.Sprintf("changed, %s -> %s", tui.FormatTokens(snapshot.TokensBefore), tui.FormatTokens(snapshot.TokensAfter))
	}
	label := fmt.Sprintf("%s / %s", snapshot.Kind, snapshot.Reason)
	if strategy := prettyCompactionStrategy(snapshot.Strategy); strategy != "" {
		label += " / " + strategy
	}
	if snapshot.CompactedCount > 0 || snapshot.KeptCount > 0 || snapshot.SplitTurn {
		var detailParts []string
		if snapshot.CompactedCount > 0 {
			detailParts = append(detailParts, fmt.Sprintf("compacted=%d", snapshot.CompactedCount))
		}
		if snapshot.KeptCount > 0 {
			detailParts = append(detailParts, fmt.Sprintf("kept=%d", snapshot.KeptCount))
		}
		if snapshot.SplitTurn {
			detailParts = append(detailParts, "split-turn")
		}
		label += " / " + strings.Join(detailParts, ",")
	}
	return fmt.Sprintf("%s, %s, at %s",
		label,
		status,
		snapshot.Timestamp.Format("15:04:05"),
	)
}

func formatRunSummary(summary agentcore.RunSummary, ok bool) string {
	if !ok {
		return "(none)"
	}
	return fmt.Sprintf("reason=%s, turns=%d, tool_calls=%d, tool_errors=%d",
		summary.EndReason,
		summary.TurnCount,
		summary.ToolCalls,
		summary.ToolErrors,
	)
}

func formatContextScope(scope string) string {
	switch scope {
	case "baseline":
		return "baseline runtime"
	case "projected":
		return "projected view"
	case "committed":
		return "committed view"
	case "recovered":
		return "overflow recovery"
	default:
		if scope == "" {
			return "(unknown)"
		}
		return scope
	}
}

func formatContextRewriteDetails(snapshot *agentcore.ContextSnapshot) string {
	if snapshot == nil {
		return "(none)"
	}
	var parts []string
	if snapshot.LastCompactedCount > 0 {
		parts = append(parts, fmt.Sprintf("compacted=%d", snapshot.LastCompactedCount))
	}
	if snapshot.LastKeptCount > 0 {
		parts = append(parts, fmt.Sprintf("kept=%d", snapshot.LastKeptCount))
	}
	if snapshot.LastSplitTurn {
		parts = append(parts, "split-turn")
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
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
