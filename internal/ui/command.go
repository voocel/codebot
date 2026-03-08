package ui

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/internal/ui/tui"
)

func (a *App) handleCommand(input string) tea.Cmd {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return nil
	}

	name := strings.ToLower(parts[0])
	args := parts[1:]

	if cmd, ok := a.registry.Lookup(strings.TrimPrefix(name, "/")); ok {
		spec := cmd.Spec()
		if err := validateCommand(a.PolicyProfile, spec, a.Session.IsRunning()); err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Command blocked: " + err.Error()))
		}
		return cmd.Run(&CommandContext{App: a}, args)
	}

	// Fallback: check skill commands (/skill:name).
	bare := strings.TrimPrefix(name, "/")
	if skillName, ok := parseSkillCommand(bare); ok {
		if skill := a.findSkill(skillName); skill != nil {
			rawArgs := strings.TrimSpace(strings.TrimPrefix(input, parts[0]))
			return a.invokeSkill(skill, rawArgs)
		}
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			fmt.Sprintf("Unknown skill: %s", skillName)))
	}

	// Fallback: check prompt templates.
	if tmpl := a.findTemplate(bare); tmpl != nil {
		rawArgs := strings.TrimSpace(strings.TrimPrefix(input, parts[0]))
		expanded := Expand(tmpl.Content, ParseArgs(rawArgs))
		return a.sendAsPrompt(expanded)
	}

	return tui.SendCommandResult(tui.CommandStyle.Render(
		fmt.Sprintf("Unknown command: %s. Type /help for available commands.", name)))
}

func validateCommand(profile policy.Profile, spec CommandSpec, isRunning bool) error {
	if err := policy.AllowCommand(profile, spec.Risk, true); err != nil {
		return err
	}
	if spec.NeedsIdle && isRunning {
		return fmt.Errorf("command requires idle agent; press Esc to abort current run")
	}
	return nil
}

// initRegistry creates the command registry with all built-in commands.
func (a *App) initRegistry() *Registry {
	r := NewRegistry()

	r.Register(NewSimple(CommandSpec{
		Name: "help", Usage: "/help", Description: "Show this help",
		Risk: policy.RiskLow,
	}, func(ctx *CommandContext, _ []string) tea.Cmd {
		return tui.SendCommandResult(ctx.App.helpText())
	}))

	r.Register(NewSimple(CommandSpec{
		Name: "clear", Usage: "/clear", Description: "Clear current context (memory only)",
		Risk: policy.RiskLow, NeedsIdle: true,
	}, func(ctx *CommandContext, _ []string) tea.Cmd {
		ctx.App.Session.ClearConversation()
		ctx.App.resetPlanState()
		return func() tea.Msg {
			return tui.CommandResultMsg{
				Text:  tui.CommandStyle.Render("Current context cleared (session history is kept)."),
				Clear: true,
			}
		}
	}))

	r.Register(NewModelCommand(a))

	r.Register(NewSimple(CommandSpec{
		Name: "compact", Usage: "/compact", Description: "Compact conversation context",
		Risk: policy.RiskMedium, NeedsIdle: true,
	}, func(ctx *CommandContext, _ []string) tea.Cmd {
		return ctx.App.cmdCompact()
	}))

	r.Register(NewSimple(CommandSpec{
		Name: "session", Usage: "/session", Description: "Show current session info",
		Risk: policy.RiskLow,
	}, func(ctx *CommandContext, _ []string) tea.Cmd {
		return ctx.App.cmdSession()
	}))

	r.Register(NewSimple(CommandSpec{
		Name: "new", Usage: "/new", Description: "Start new session",
		Risk: policy.RiskMedium, NeedsIdle: true,
	}, func(ctx *CommandContext, _ []string) tea.Cmd {
		return ctx.App.cmdNew()
	}))

	r.Register(NewSimple(CommandSpec{
		Name: "resume", Usage: "/resume [id|index]", Description: "List sessions or resume by id/index",
		Risk: policy.RiskMedium, NeedsIdle: true,
	}, func(ctx *CommandContext, args []string) tea.Cmd {
		return ctx.App.cmdResume(args)
	}))

	r.Register(NewSimple(CommandSpec{
		Name: "settings", Usage: "/settings", Description: "Show current settings",
		Risk: policy.RiskLow,
	}, func(ctx *CommandContext, _ []string) tea.Cmd {
		return ctx.App.cmdSettings()
	}))

	r.Register(NewSimple(CommandSpec{
		Name: "mcp", Usage: "/mcp", Description: "Show MCP server status",
		Risk: policy.RiskLow,
	}, func(ctx *CommandContext, _ []string) tea.Cmd {
		return ctx.App.cmdMCP()
	}))

	r.Register(NewSimple(CommandSpec{
		Name: "copy", Usage: "/copy", Description: "Copy last response to clipboard",
		Risk: policy.RiskLow,
	}, func(ctx *CommandContext, _ []string) tea.Cmd {
		return ctx.App.cmdCopy()
	}))

	r.Register(NewSimple(CommandSpec{
		Name: "plan", Usage: "/plan [cancel|<task>]", Description: "Enter plan mode or manage plans",
		Risk: policy.RiskLow, NeedsIdle: true,
	}, func(ctx *CommandContext, args []string) tea.Cmd {
		return ctx.App.cmdPlan(args)
	}))

	r.Register(NewSimple(CommandSpec{
		Name: "reload", Usage: "/reload", Description: "Reload skills, prompts, and context files",
		Risk: policy.RiskLow, NeedsIdle: true,
	}, func(ctx *CommandContext, _ []string) tea.Cmd {
		return ctx.App.cmdReload()
	}))

	quitFn := func(_ *CommandContext, _ []string) tea.Cmd {
		return func() tea.Msg { return tui.CommandResultMsg{Quit: true} }
	}
	r.Register(NewSimple(CommandSpec{
		Name: "exit", Aliases: []string{"quit", "q"},
		Usage: "/exit", Description: "Quit",
		Risk: policy.RiskLow,
	}, quitFn))

	return r
}

func (a *App) helpText() string {
	var sb strings.Builder
	sb.WriteString("Available commands:\n")
	for _, cmd := range a.registry.All() {
		spec := cmd.Spec()
		if spec.Hidden {
			continue
		}
		risk := spec.Risk
		if risk == "" {
			risk = policy.RiskLow
		}
		if spec.NeedsIdle {
			fmt.Fprintf(&sb, "  %-17s %s [%s, idle]\n", spec.Usage, spec.Description, string(risk))
			continue
		}
		fmt.Fprintf(&sb, "  %-17s %s [%s]\n", spec.Usage, spec.Description, string(risk))
	}

	sb.WriteString(strings.TrimSpace(`

Keyboard shortcuts:
  Enter             Send message
  Esc               Abort running agent
  Ctrl+C            Quit
`))

	// Append prompt templates if any.
	if len(a.Templates) > 0 {
		sb.WriteString("\n\nCustom prompts:\n")
		for _, t := range a.Templates {
			desc := t.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&sb, "  /%-16s %s (%s)\n", t.Name, desc, t.Source)
		}
	}

	// Append skills if any.
	if len(a.Skills) > 0 {
		sb.WriteString("\n\nSkills:\n")
		for _, s := range a.Skills {
			desc := s.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&sb, "  /skill:%-11s %s (%s)\n", s.Name, desc, s.Source)
		}
	}

	return tui.CommandStyle.Render(sb.String())
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
		// Fallback: try direct provider/model if registry fails
		prov, model := config.ParseModelID(pattern)
		if prov == "" {
			prov = a.Session.Provider()
		}
		if setErr := a.Session.SetModel(prov, model); setErr != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render(
				fmt.Sprintf("Failed to switch model: %v", setErr)))
		}
		resolved = pattern
	}

	return func() tea.Msg {
		return tui.CommandResultMsg{
			Text:     tui.CommandStyle.Render(fmt.Sprintf("Switched to model: %s", resolved)),
			NewModel: resolved,
		}
	}
}

func (a *App) cmdCompact() tea.Cmd {
	if err := a.Session.Compact(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Compaction failed: " + err.Error()))
	}
	return tui.SendCommandResult(tui.CommandStyle.Render("Context compacted."))
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
			Text:  tui.CommandStyle.Render("New session started."),
			Clear: true,
		}
	}
}

func (a *App) cmdResume(args []string) tea.Cmd {
	sessions, err := a.Session.ListSessions()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to list sessions: " + err.Error()))
	}
	if len(sessions) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("No sessions found."))
	}

	if len(args) > 0 {
		target := strings.TrimSpace(args[0])
		if n, convErr := strconv.Atoi(target); convErr == nil {
			if n < 1 || n > len(sessions) {
				return tui.SendCommandResult(tui.ErrorStyle.Render(
					fmt.Sprintf("Invalid index %d (range: 1-%d)", n, len(sessions))))
			}
			target = sessions[n-1].ID
		}

		if err := a.Session.SwitchSession(target); err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to resume session: " + err.Error()))
		}
		a.resetPlanState()

		resumed := target
		for _, s := range sessions {
			if s.ID == target {
				if s.Name != "" {
					resumed = s.Name + " (" + s.ID + ")"
				}
				break
			}
		}

		return func() tea.Msg {
			return tui.CommandResultMsg{
				Text:  tui.CommandStyle.Render(fmt.Sprintf("Resumed session: %s", resumed)),
				Clear: true,
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("Recent sessions:\n")
	limit := min(len(sessions), 10)
	for i := range limit {
		s := sessions[i]
		name := s.ID
		if len(name) > 8 {
			name = name[:8]
		}
		if s.Name != "" {
			name = s.Name
		}
		line := fmt.Sprintf("  %d. %-16s (%d msgs)  %s  [id:%s]",
			i+1, name, s.MessageCount, s.Updated.Format("01-02 15:04"), s.ID)
		if s.FirstMessage != "" {
			line += "  - " + s.FirstMessage
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\nUse /resume <index> or /resume <id>")
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
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
	info := fmt.Sprintf("Provider: %s\nModel: %s\nAPI Key: %s\nBase URL: %s\nThinking level: %s\nContext window: %d\nAuto compaction: %v\nMax turns: %d\nConfig: %s",
		s.Provider, a.Session.ModelName(), masked, baseURL,
		thinking, s.ContextWindow, s.AutoCompaction, s.MaxTurns, config.SettingsPath(a.Cwd))
	return tui.SendCommandResult(tui.CommandStyle.Render(info))
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
	return tui.SendCommandResult(tui.CommandStyle.Render(fmt.Sprintf("Copied %d characters to clipboard.", n)))
}

func (a *App) cmdMCP() tea.Cmd {
	if a.MCPManager == nil {
		return tui.SendCommandResult(tui.CommandStyle.Render("No MCP servers configured."))
	}

	servers := a.MCPManager.Status(context.Background())
	if len(servers) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("No MCP servers found."))
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
		} else {
			fmt.Fprintf(&sb, "%s %-20s %d tools\n",
				tui.DiffAddStyle.Render("●"), s.Name, s.ToolCount)
			totalTools += s.ToolCount
			connected++
		}
	}
	fmt.Fprintf(&sb, "\nTotal: %d connected, %d failed, %d tools", connected, failed, totalTools)
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
}

func (a *App) cmdReload() tea.Cmd {
	a.Session.Reload()
	a.Templates = config.LoadPromptTemplates(a.Cwd)
	a.Skills = a.Session.Skills()
	return tui.SendCommandResult(tui.CommandStyle.Render(
		fmt.Sprintf("Reloaded: %d skills, %d prompts.", len(a.Skills), len(a.Templates))))
}

// findTemplate returns the first template matching name (case-insensitive), or nil.
func (a *App) findTemplate(name string) *config.PromptTemplate {
	lower := strings.ToLower(name)
	for i := range a.Templates {
		if strings.ToLower(a.Templates[i].Name) == lower {
			return &a.Templates[i]
		}
	}
	return nil
}

// sendAsPrompt sends expanded template text as a user message to the agent.
func (a *App) sendAsPrompt(text string) tea.Cmd {
	return func() tea.Msg {
		return tui.PromptMsg{Text: text}
	}
}

// ---------------------------------------------------------------------------
// Skill invocation
// ---------------------------------------------------------------------------

// parseSkillCommand checks if name matches "skill:xxx" and returns the skill name.
func parseSkillCommand(name string) (string, bool) {
	after, ok := strings.CutPrefix(name, "skill:")
	if !ok || after == "" {
		return "", false
	}
	return after, true
}

// findSkill returns the first skill matching name (case-insensitive), or nil.
func (a *App) findSkill(name string) *config.Skill {
	lower := strings.ToLower(name)
	for i := range a.Skills {
		if strings.ToLower(a.Skills[i].Name) == lower {
			return &a.Skills[i]
		}
	}
	return nil
}

// invokeSkill reads the skill file, strips frontmatter, wraps it in a <skill> XML block,
// and sends it as a prompt to the agent.
func (a *App) invokeSkill(skill *config.Skill, userArgs string) tea.Cmd {
	data, err := os.ReadFile(skill.FilePath)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			fmt.Sprintf("Failed to read skill file: %v", err)))
	}

	body := strings.TrimSpace(config.StripFrontmatter(string(data)))

	var sb strings.Builder
	fmt.Fprintf(&sb, "<skill name=%q location=%q>\n", skill.Name, skill.FilePath)
	fmt.Fprintf(&sb, "References are relative to %s.\n\n", skill.BaseDir)
	sb.WriteString(body)
	sb.WriteString("\n</skill>")

	if userArgs != "" {
		sb.WriteString("\n\n")
		sb.WriteString(userArgs)
	}

	return a.sendAsPrompt(sb.String())
}
