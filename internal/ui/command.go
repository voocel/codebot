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
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/cron"
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
			fmt.Sprintf("Unknown command: /%s. 输入 / 浏览命令，或用 /help 查看完整列表。", inv.Name)))
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
			return fmt.Errorf("command requires idle agent; press Esc to abort current run")
		}
		return nil
	}
	return engine.ApproveCommand(ctx, approval.CommandRequest{
		Name:      spec.Name,
		Category:  approval.NormalizeCommandCategory(spec.Category),
		NeedsIdle: spec.NeedsIdle,
		IsRunning: isRunning,
		Summary:   "/" + spec.Name,
	})
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
		NewSimple(CommandSpec{
			Name: "help", Usage: "/help", Description: "Show this help",
			Category: "info", Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return tui.SendCommandResult(ctx.App.helpText())
		}),
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
		NewSimple(CommandSpec{
			Name: "new", Usage: "/new", Description: "Start new session",
			Category: "session", NeedsIdle: true, Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return ctx.App.cmdNew()
		}),
		NewResumeCommand(a),
		NewTasksCommand(a),
		NewBtwCommand(a),
		NewSimple(CommandSpec{
			Name: "settings", Usage: "/settings", Description: "Show current settings",
			Category: "info", Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return ctx.App.cmdSettings()
		}),
		NewSimple(CommandSpec{
			Name: "mcp", Usage: "/mcp", Description: "Show MCP server status",
			Category: "info", Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return ctx.App.cmdMCP()
		}),
		NewSimple(CommandSpec{
			Name: "debug-harness", Usage: "/debug-harness", Description: "Show harness runtime diagnostics",
			Category: "info", Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return ctx.App.cmdDebugHarness()
		}),
		NewSimple(CommandSpec{
			Name: "copy", Usage: "/copy", Description: "Copy last response to clipboard",
			Category: "info", Kind: CommandKindBuiltin,
		}, func(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
			return ctx.App.cmdCopy()
		}),
		NewSimple(CommandSpec{
			Name: "plan", Usage: "/plan [cancel|<task>]", Description: "Enter plan mode or manage plans",
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

func (a *App) helpText() string {
	groups := map[CommandKind][]Command{
		CommandKindBuiltin: nil,
		CommandKindCustom:  nil,
		CommandKindSkill:   nil,
	}
	for _, cmd := range a.registry.All() {
		spec := a.registry.EffectiveSpec(cmd)
		if spec.Hidden {
			continue
		}
		groups[spec.Kind] = append(groups[spec.Kind], cmd)
	}

	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("247"))
	sectionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("249"))
	var sb strings.Builder
	sb.WriteString(headerStyle.Render("Available commands (/ opens the command palette):"))
	a.renderCommandGroup(&sb, "Built-in", groups[CommandKindBuiltin])
	a.renderCommandGroup(&sb, "Custom commands", groups[CommandKindCustom])
	a.renderCommandGroup(&sb, "Skills", groups[CommandKindSkill])

	sb.WriteString("\n\n")
	sb.WriteString(sectionStyle.Render("Keyboard shortcuts:"))
	sb.WriteString("\n")
	sb.WriteString(tui.MutedStyle.Render(strings.TrimSpace(`

  Enter             Send message
  Esc               Abort running agent
  Ctrl+C            Quit
`)))

	return sb.String()
}

func (a *App) renderCommandGroup(sb *strings.Builder, title string, commands []Command) {
	if len(commands) == 0 {
		return
	}

	sectionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("249"))
	usageStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("247"))
	metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))

	sb.WriteString("\n\n")
	sb.WriteString(sectionStyle.Render(title + ":"))
	sb.WriteString("\n")

	for _, cmd := range commands {
		spec := a.registry.EffectiveSpec(cmd)
		category := strings.TrimSpace(spec.Category)
		if category == "" {
			category = "prompt"
		}

		usage := spec.Usage
		if usage == "" {
			usage = "/" + spec.Name
		}

		desc := spec.Description
		if desc == "" {
			desc = "(no description)"
		}
		if spec.Source != "" {
			desc += " (" + spec.Source + ")"
		}

		tags := []string{category}
		if spec.NeedsIdle {
			tags = append(tags, "idle")
		}
		if len(spec.Aliases) > 0 {
			tags = append(tags, "aliases: "+strings.Join(spec.Aliases, ","))
		}

		sb.WriteString("  ")
		sb.WriteString(usageStyle.Render(fmt.Sprintf("%-24s", usage)))
		sb.WriteString(" ")
		sb.WriteString(descStyle.Render(desc))
		sb.WriteString(" ")
		sb.WriteString(metaStyle.Render("[" + strings.Join(tags, ", ") + "]"))
		sb.WriteString("\n")
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
					if strings.EqualFold(m.ID, currentModel) {
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
			Text:     tui.SystemMsgStyle.Render(fmt.Sprintf("Switched to model: %s", resolved)),
			NewModel: resolved,
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
	if err := a.Session.NewSession(); err != nil {
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

func (a *App) cmdSettings() tea.Cmd {
	s := a.Session.Settings()
	baseURL := a.Session.BaseURL()
	if baseURL == "" {
		baseURL = "(default)"
	}
	thinking := s.ThinkingLevel
	if thinking == "" {
		thinking = "(unset)"
	}
	apiKey := a.Session.APIKey()
	masked := maskKey(apiKey)

	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("247"))
	metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("248"))

	autoCompact := "off"
	if s.AutoCompaction {
		autoCompact = "on"
	}

	var sb strings.Builder
	renderRow := func(label, value string) {
		sb.WriteString(labelStyle.Render(fmt.Sprintf("%-16s", label)))
		sb.WriteString(" ")
		sb.WriteString(valueStyle.Render(value))
		sb.WriteString("\n")
	}
	renderMetaRow := func(label, value string) {
		sb.WriteString(labelStyle.Render(fmt.Sprintf("%-16s", label)))
		sb.WriteString(" ")
		sb.WriteString(metaStyle.Render(value))
		sb.WriteString("\n")
	}

	renderRow("Provider", s.Provider)
	renderRow("Model", a.Session.ModelName())
	renderRow("API key", masked)
	renderRow("Base URL", baseURL)
	sb.WriteString("\n")
	renderRow("Thinking", thinking)
	renderRow("Context", tui.FormatTokens(s.ContextWindow))
	renderRow("Auto compact", autoCompact)
	renderRow("Max turns", fmt.Sprintf("%d", s.MaxTurns))
	renderMetaRow("Config", config.SettingsPath(a.Cwd))

	return tui.SendCommandResult(strings.TrimRight(sb.String(), "\n"))
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

func (a *App) cmdDebugHarness() tea.Cmd {
	metrics := a.Session.RuntimeMetrics()
	lastTurn := a.Session.LastTurnOutcome()
	lastRunSummary, hasRunSummary := a.Session.LastRunSummary()
	recentTools := a.Session.RecentToolCalls(5)
	lastReminder, hasReminder := a.Session.LastReminder()
	lastCompaction, hasCompaction := a.Session.LastCompaction()
	contextUsage := a.Session.ContextUsage()
	toolCalls := 0
	if hasRunSummary {
		toolCalls = lastRunSummary.ToolCalls
	}

	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("247"))
	metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("248"))

	var sb strings.Builder
	renderSection := func(title, meta string) {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(title)
		sb.WriteString("\n")
		if meta != "" {
			sb.WriteString(metaStyle.Render(meta))
			sb.WriteString("\n")
		}
	}
	renderRow := func(label, value string) {
		sb.WriteString(labelStyle.Render(fmt.Sprintf("%-22s", label)))
		sb.WriteString(" ")
		sb.WriteString(valueStyle.Render(value))
		sb.WriteString("\n")
	}
	renderMetaRow := func(label, value string) {
		sb.WriteString(labelStyle.Render(fmt.Sprintf("%-22s", label)))
		sb.WriteString(" ")
		sb.WriteString(metaStyle.Render(value))
		sb.WriteString("\n")
	}
	renderBlock := func(label string, lines []string) {
		if len(lines) == 0 {
			renderMetaRow(label, "(none)")
			return
		}
		for i, line := range lines {
			currentLabel := ""
			if i == 0 {
				currentLabel = label
			}
			sb.WriteString(labelStyle.Render(fmt.Sprintf("%-22s", currentLabel)))
			sb.WriteString(" ")
			sb.WriteString(valueStyle.Render(line))
			sb.WriteString("\n")
		}
	}

	sb.WriteString("Harness Debug\n")
	renderSection("Last turn", "Only the most recent completed agent run. If the last run was just a final reply, tool calls can be 0.")
	renderRow("Assistant responded", formatBool(lastTurn.AssistantResponded))
	renderRow("Completion claim", formatBool(lastTurn.CompletionClaim))
	renderRow("Tool calls", fmt.Sprintf("%d", toolCalls))
	renderRow("Read-only tools", fmt.Sprintf("%d", lastTurn.ReadOnlyToolCalls))
	renderRow("Write-like tools", fmt.Sprintf("%d", lastTurn.WriteLikeToolCalls))
	renderRow("Task mutations", fmt.Sprintf("%d", lastTurn.TaskMutations))
	renderMetaRow("Run summary", formatRunSummary(lastRunSummary, hasRunSummary))

	renderSection("Recent activity", "Recent runtime events inside the current loaded session.")
	renderBlock("Recent tool calls", formatRecentToolCalls(recentTools))
	renderMetaRow("Last reminder", formatLastReminder(lastReminder, hasReminder))
	renderMetaRow("Last compaction", formatLastCompaction(lastCompaction, hasCompaction))

	renderSection("Session metrics", "Accumulates since this session was created or loaded in the current process.")
	renderRow("Reminders total", fmt.Sprintf("%d", metrics.ReminderTotal))
	renderMetaRow("Reminder kinds", formatReminderCounts(metrics.ReminderByKind))
	renderRow("Compactions total", fmt.Sprintf("%d", metrics.CompactionTotal))
	renderRow("Compactions changed", fmt.Sprintf("%d", metrics.CompactionChanged))
	renderRow("Compaction saved", tui.FormatTokens(metrics.CompactionSaved))
	renderMetaRow("Compaction kinds", formatCompactionCounts(metrics.CompactionByKind))
	renderMetaRow("Saved by compaction", formatCompactionSavings(metrics.CompactionSavedByKind))

	if contextUsage != nil {
		renderSection("Current context", "Live context window usage for the current agent state.")
		renderRow("Context used", fmt.Sprintf("%s (%.1f%%)", tui.FormatTokens(contextUsage.Tokens), contextUsage.Percent))
		renderRow("Context window", tui.FormatTokens(contextUsage.ContextWindow))
	}

	return tui.SendCommandResult(tui.CommandStyle.Render(strings.TrimRight(sb.String(), "\n")))
}

func (a *App) cmdReload() tea.Cmd {
	a.Session.Reload()
	a.Commands = config.LoadFileCommands(a.Cwd)
	a.Skills = a.Session.Skills()
	if found := a.Session.ToolsByName("Skill"); len(found) > 0 {
		if st, ok := found[0].(*tools.SkillTool); ok {
			st.SetSkills(a.Skills)
		}
	}
	a.rebuildRegistry()
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(
		fmt.Sprintf("Reloaded: %d commands, %d skills.", len(a.Commands), len(a.Skills))))
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
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = os.Getenv("VISUAL")
		}
		if editor == "" {
			editor = "vi"
		}
		c := exec.Command(editor, memPath)
		return tea.ExecProcess(c, func(err error) tea.Msg {
			if err != nil {
				return tui.CommandResultMsg{Text: tui.ErrorStyle.Render("editor: " + err.Error())}
			}
			a.Session.Reload()
			return tui.CommandResultMsg{Text: tui.SystemMsgStyle.Render("Memory reloaded.")}
		})
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
		agent.ReminderUnfinishedTasks,
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
	return fmt.Sprintf("%s / %s, %s, at %s",
		snapshot.Kind,
		snapshot.Reason,
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

// sendAsPrompt sends expanded template text as a user message to the agent.
func (a *App) sendAsPrompt(text string) tea.Cmd {
	return func() tea.Msg {
		return tui.PromptMsg{Text: text}
	}
}
