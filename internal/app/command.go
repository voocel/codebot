package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/internal/prompt"
	"github.com/voocel/codebot/internal/session"
	"github.com/voocel/codebot/tui"
)

type commandSpec struct {
	Usage       string
	Description string
	Risk        policy.CommandRisk
	NeedsIdle   bool
	Hidden      bool
	Run         func(args []string) tea.Cmd
}

func (a *App) handleCommand(input string) tea.Cmd {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return nil
	}

	cmd := strings.ToLower(parts[0])
	args := parts[1:]
	registry := a.commandRegistry()

	spec, ok := registry[cmd]
	if ok {
		if err := validateCommand(a.PolicyProfile, spec, a.Session.IsRunning()); err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Command blocked: " + err.Error()))
		}
		return spec.Run(args)
	}

	// Fallback: check prompt templates.
	name := strings.TrimPrefix(cmd, "/")
	if tmpl := a.findTemplate(name); tmpl != nil {
		rawArgs := strings.TrimSpace(strings.TrimPrefix(input, parts[0]))
		expanded := prompt.Expand(tmpl.Content, prompt.ParseArgs(rawArgs))
		return a.sendAsPrompt(expanded)
	}

	return tui.SendCommandResult(tui.CommandStyle.Render(
		fmt.Sprintf("Unknown command: %s. Type /help for available commands.", cmd)))
}

func validateCommand(profile policy.Profile, spec commandSpec, isRunning bool) error {
	if err := policy.AllowCommand(profile, spec.Risk, true); err != nil {
		return err
	}
	if spec.NeedsIdle && isRunning {
		return fmt.Errorf("command requires idle agent; press Esc to abort current run")
	}
	return nil
}

func (a *App) commandRegistry() map[string]commandSpec {
	return map[string]commandSpec{
		"/help": {
			Usage:       "/help",
			Description: "Show this help",
			Risk:        policy.RiskLow,
			Run: func(_ []string) tea.Cmd {
				return tui.SendCommandResult(a.helpText())
			},
		},
		"/clear": {
			Usage:       "/clear",
			Description: "Clear current context (memory only)",
			Risk:        policy.RiskLow,
			NeedsIdle:   true,
			Run: func(_ []string) tea.Cmd {
				a.Session.ClearConversation()
				return func() tea.Msg {
					return tui.CommandResultMsg{
						Text:  tui.CommandStyle.Render("Current context cleared (session history is kept)."),
						Clear: true,
					}
				}
			},
		},
		"/model": {
			Usage:       "/model <name>",
			Description: "Switch model",
			Risk:        policy.RiskLow,
			NeedsIdle:   true,
			Run: func(args []string) tea.Cmd {
				return a.cmdModel(args)
			},
		},
		"/compact": {
			Usage:       "/compact",
			Description: "Compact conversation context",
			Risk:        policy.RiskMedium,
			NeedsIdle:   true,
			Run: func(_ []string) tea.Cmd {
				return a.cmdCompact()
			},
		},
		"/session": {
			Usage:       "/session",
			Description: "Show current session info",
			Risk:        policy.RiskLow,
			Run: func(_ []string) tea.Cmd {
				return a.cmdSession()
			},
		},
		"/name": {
			Usage:       "/name <name>",
			Description: "Name current session",
			Risk:        policy.RiskLow,
			Run: func(args []string) tea.Cmd {
				return a.cmdName(args)
			},
		},
		"/new": {
			Usage:       "/new",
			Description: "Start new session",
			Risk:        policy.RiskMedium,
			NeedsIdle:   true,
			Run: func(_ []string) tea.Cmd {
				return a.cmdNew()
			},
		},
		"/resume": {
			Usage:       "/resume [id|index]",
			Description: "List sessions or resume by id/index",
			Risk:        policy.RiskMedium,
			NeedsIdle:   true,
			Run: func(args []string) tea.Cmd {
				return a.cmdResume(args)
			},
		},
		"/fork": {
			Usage:       "/fork <id>",
			Description: "Fork conversation from entry ID",
			Risk:        policy.RiskMedium,
			NeedsIdle:   true,
			Run: func(args []string) tea.Cmd {
				return a.cmdFork(args)
			},
		},
		"/tree": {
			Usage:       "/tree",
			Description: "Show session tree structure",
			Risk:        policy.RiskLow,
			Run: func(_ []string) tea.Cmd {
				return a.cmdTree()
			},
		},
		"/label": {
			Usage:       "/label [id] [text]",
			Description: "List, set, or clear labels",
			Risk:        policy.RiskLow,
			Run: func(args []string) tea.Cmd {
				return a.cmdLabel(args)
			},
		},
		"/settings": {
			Usage:       "/settings",
			Description: "Show current settings",
			Risk:        policy.RiskLow,
			Run: func(_ []string) tea.Cmd {
				return a.cmdSettings()
			},
		},
		"/copy": {
			Usage:       "/copy",
			Description: "Copy last response to clipboard",
			Risk:        policy.RiskLow,
			Run: func(_ []string) tea.Cmd {
				return a.cmdCopy()
			},
		},
		"/models": {
			Usage:       "/models [filter]",
			Description: "List available models",
			Risk:        policy.RiskLow,
			Run: func(args []string) tea.Cmd {
				return a.cmdModels(args)
			},
		},
		"/thinking": {
			Usage:       "/thinking [off|minimal|low|medium|high|xhigh]",
			Description: "Show or set thinking level",
			Risk:        policy.RiskLow,
			Run: func(args []string) tea.Cmd {
				return a.cmdThinking(args)
			},
		},
		"/plan": {
			Usage:       "/plan [execute|cancel|list|show]",
			Description: "Enter plan mode or manage plans",
			Risk:        policy.RiskLow,
			NeedsIdle:   true,
			Run: func(args []string) tea.Cmd {
				return a.cmdPlan(args)
			},
		},
		"/todos": {
			Usage:       "/todos",
			Description: "Show plan steps and progress",
			Risk:        policy.RiskLow,
			Run: func(_ []string) tea.Cmd {
				return a.cmdTodos()
			},
		},
		"/exit": {
			Usage:       "/exit",
			Description: "Quit",
			Risk:        policy.RiskLow,
			Run: func(_ []string) tea.Cmd {
				return func() tea.Msg { return tui.CommandResultMsg{Quit: true} }
			},
		},
		"/quit": {
			Usage:       "/quit",
			Description: "Quit",
			Risk:        policy.RiskLow,
			Hidden:      true,
			Run: func(_ []string) tea.Cmd {
				return func() tea.Msg { return tui.CommandResultMsg{Quit: true} }
			},
		},
	}
}

func (a *App) helpText() string {
	registry := a.commandRegistry()
	var keys []string
	for k, spec := range registry {
		if spec.Hidden {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString("Available commands:\n")
	for _, k := range keys {
		spec := registry[k]
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

	return tui.CommandStyle.Render(sb.String())
}

func (a *App) cmdModel(args []string) tea.Cmd {
	currentModel := a.Session.ModelName()
	if len(args) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render(
			fmt.Sprintf("Current model: %s. Usage: /model <name>", currentModel)))
	}

	pattern := strings.Join(args, " ")
	resolved, err := a.Session.ResolveAndSetModel(pattern)
	if err != nil {
		// Fallback: try direct provider/model if registry fails
		prov := a.Session.Provider()
		apiKey := a.Session.APIKey()
		if setErr := a.Session.SetModel(prov, pattern, apiKey); setErr != nil {
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
			cost, formatTokenCount(inTok), formatTokenCount(outTok))
	}

	return tui.SendCommandResult(tui.CommandStyle.Render(text))
}

func (a *App) cmdName(args []string) tea.Cmd {
	if len(args) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("Usage: /name <session name>"))
	}
	name := strings.Join(args, " ")
	if err := a.Session.SetSessionName(name); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to set name: " + err.Error()))
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(fmt.Sprintf("Session named: %s", name)))
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
		s.DefaultProvider, a.Session.ModelName(), masked, baseURL,
		thinking, s.ContextWindow, s.AutoCompaction, s.MaxTurns, config.SettingsPath(a.Cwd))
	return tui.SendCommandResult(tui.CommandStyle.Render(info))
}

func (a *App) cmdThinking(args []string) tea.Cmd {
	current := a.Session.Settings().ThinkingLevel
	if current == "" {
		current = "off"
	}
	if len(args) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render(
			fmt.Sprintf("Thinking level: %s\nUsage: /thinking [off|minimal|low|medium|high|xhigh]", current)))
	}

	level := strings.ToLower(strings.TrimSpace(args[0]))
	switch agentcore.ThinkingLevel(level) {
	case agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow,
		agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh:
	default:
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			"Invalid thinking level. Use one of: off, minimal, low, medium, high, xhigh"))
	}

	a.Session.SetThinkingLevel(agentcore.ThinkingLevel(level))
	return tui.SendCommandResult(tui.CommandStyle.Render(
		fmt.Sprintf("Thinking level set to: %s", level)))
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func formatTokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func (a *App) cmdFork(args []string) tea.Cmd {
	if len(args) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render(
			"Usage: /fork <entry-id>\nUse /tree to see available entry IDs."))
	}
	entryID := args[0]
	if err := a.Session.Fork(entryID); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Fork failed: " + err.Error()))
	}
	return func() tea.Msg {
		return tui.CommandResultMsg{
			Text:  tui.CommandStyle.Render(fmt.Sprintf("Forked from entry: %s", entryID)),
			Clear: true,
		}
	}
}

func (a *App) cmdTree() tea.Cmd {
	root, currentLeaf, err := a.Session.SessionTree()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Build tree failed: " + err.Error()))
	}

	var sb strings.Builder
	sb.WriteString("Session tree:\n")
	renderTree(&sb, root, "", true, currentLeaf)
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
}

func renderTree(sb *strings.Builder, node *session.TreeNode, prefix string, isLast bool, currentLeaf string) {
	if node == nil {
		return
	}

	// Draw connector
	connector := "├── "
	if isLast {
		connector = "└── "
	}
	if prefix == "" {
		connector = ""
	}

	// Format entry label
	label := formatTreeEntry(node, currentLeaf)
	fmt.Fprintf(sb, "%s%s%s\n", prefix, connector, label)

	// Child prefix
	childPrefix := prefix
	if prefix != "" {
		if isLast {
			childPrefix += "    "
		} else {
			childPrefix += "│   "
		}
	}

	for i, child := range node.Children {
		renderTree(sb, child, childPrefix, i == len(node.Children)-1, currentLeaf)
	}
}

func formatTreeEntry(node *session.TreeNode, currentLeaf string) string {
	e := node.Entry
	marker := ""
	if e.ID == currentLeaf {
		marker = " *"
	}

	switch e.Kind {
	case session.EntryHeader:
		return fmt.Sprintf("[%s] session start%s", e.ID, marker)
	case session.EntryMessage:
		// Try to extract role from data
		var msg struct {
			Role string `json:"role"`
		}
		role := "msg"
		if json.Unmarshal(e.Data, &msg) == nil && msg.Role != "" {
			role = msg.Role
		}
		return fmt.Sprintf("[%s] %s%s", e.ID, role, marker)
	case session.EntryModelChange:
		var mc session.ModelChange
		if json.Unmarshal(e.Data, &mc) == nil {
			return fmt.Sprintf("[%s] model: %s%s", e.ID, mc.Model, marker)
		}
		return fmt.Sprintf("[%s] model change%s", e.ID, marker)
	case session.EntryCompaction:
		return fmt.Sprintf("[%s] compaction%s", e.ID, marker)
	case session.EntryThinkingChange:
		return fmt.Sprintf("[%s] thinking change%s", e.ID, marker)
	case session.EntrySessionInfo:
		return fmt.Sprintf("[%s] info%s", e.ID, marker)
	case session.EntryBranchSummary:
		return fmt.Sprintf("[%s] branch summary%s", e.ID, marker)
	case session.EntryLabel:
		var l session.Label
		if json.Unmarshal(e.Data, &l) == nil {
			return fmt.Sprintf("[%s] label: %s → %q%s", e.ID, l.TargetID, l.Label, marker)
		}
		return fmt.Sprintf("[%s] label%s", e.ID, marker)
	default:
		return fmt.Sprintf("[%s] %s%s", e.ID, e.Kind, marker)
	}
}

func (a *App) cmdLabel(args []string) tea.Cmd {
	// /label — list all labels
	if len(args) == 0 {
		labels, err := a.Session.Labels()
		if err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to load labels: " + err.Error()))
		}
		if len(labels) == 0 {
			return tui.SendCommandResult(tui.CommandStyle.Render("No labels set. Usage: /label <entry-id> <text>"))
		}
		var sb strings.Builder
		sb.WriteString("Labels:\n")
		for id, text := range labels {
			fmt.Fprintf(&sb, "  [%s] %s\n", id, text)
		}
		return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
	}

	targetID := args[0]
	// /label <id> — clear label
	if len(args) == 1 {
		if err := a.Session.SetLabel(targetID, ""); err != nil {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to clear label: " + err.Error()))
		}
		return tui.SendCommandResult(tui.CommandStyle.Render(fmt.Sprintf("Label cleared for entry %s", targetID)))
	}

	// /label <id> <text> — set label
	text := strings.Join(args[1:], " ")
	if err := a.Session.SetLabel(targetID, text); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Failed to set label: " + err.Error()))
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(fmt.Sprintf("Label set: [%s] %s", targetID, text)))
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

func (a *App) cmdModels(args []string) tea.Cmd {
	reg := a.Session.Registry()
	if reg == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("No model registry configured."))
	}

	filter := strings.Join(args, " ")
	models := reg.List(filter)
	if len(models) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("No models found."))
	}

	currentModel := a.Session.ModelName()
	var sb strings.Builder
	sb.WriteString("Available models:\n")
	for _, m := range models {
		marker := "  "
		if strings.EqualFold(m.ID, currentModel) {
			marker = "* "
		}
		ctx := formatTokenCount(m.ContextWindow)
		reasoning := ""
		if m.Reasoning {
			reasoning = "  reasoning"
		}
		fmt.Fprintf(&sb, "%s%-12s/%-30s %-20s %6s%s\n",
			marker, m.Provider, m.ID, m.Name, ctx, reasoning)
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
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
