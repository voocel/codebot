package ui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/internal/ui/tui"
)

// ModelCommand implements InteractiveCommand for /model.
// Without arguments it opens an interactive selector overlay;
// with arguments it switches directly.
type ModelCommand struct {
	app   *App
	state *modelSelectState // non-nil when interactive overlay is active
}

// modelSelectEntry represents a single selectable model in the list.
type modelSelectEntry struct {
	provider string // provider config key (e.g. "zenmux", "anthropic")
	model    string // model name as configured
}

// modelGroup tracks a provider's range in the flat entries list.
type modelGroup struct {
	name     string // provider key
	startIdx int    // first entry index
	count    int    // number of entries
}

type modelSelectState struct {
	entries     []modelSelectEntry
	groups      []modelGroup
	current     string // current model ID
	currentProv string // current provider key
	cursor      int
	thinkLevels []string
	thinkIdx    int
}

func NewModelCommand(app *App) *ModelCommand {
	return &ModelCommand{app: app}
}

func (c *ModelCommand) Spec() CommandSpec {
	return CommandSpec{
		Name:        "model",
		Aliases:     []string{"m"},
		Usage:       "/model [name]",
		Description: "Show or switch model",
		Risk:        policy.RiskLow,
		NeedsIdle:   true,
		Kind:        CommandKindBuiltin,
	}
}

func (c *ModelCommand) Run(ctx *CommandContext, inv CommandInvocation) tea.Cmd {
	if len(inv.Args) > 0 {
		return ctx.App.cmdModel(inv.Args)
	}

	// Build model list from all configured providers.
	settings := ctx.App.Session.Settings()
	entries, groups := buildModelList(settings.Providers)
	if len(entries) == 0 {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			"No models configured. Add models to your providers in .codebot/settings.json"))
	}

	currentModel := ctx.App.Session.ModelName()
	currentProv := ctx.App.Session.Provider()

	// Position cursor on current model.
	cursor := 0
	for i, e := range entries {
		if e.provider == currentProv && strings.EqualFold(e.model, currentModel) {
			cursor = i
			break
		}
	}

	reg := ctx.App.Session.Registry()
	var thinkLevels []string
	if reg != nil {
		thinkLevels = reg.AvailableThinkingLevels(entries[cursor].model)
	} else {
		thinkLevels = []string{"off"}
	}
	thinkIdx := currentThinkingIndex(ctx.App, thinkLevels)

	c.state = &modelSelectState{
		entries:     entries,
		groups:      groups,
		current:     currentModel,
		currentProv: currentProv,
		cursor:      cursor,
		thinkLevels: thinkLevels,
		thinkIdx:    thinkIdx,
	}
	ctx.App.registry.SetOverlay(c)
	return nil
}

func (c *ModelCommand) Active() bool { return c.state != nil }

func (c *ModelCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if c.state == nil {
		return false, nil
	}
	s := c.state

	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
			c.refreshThinking()
		}
		return true, nil

	case "down", "j":
		if s.cursor < len(s.entries)-1 {
			s.cursor++
			c.refreshThinking()
		}
		return true, nil

	case "left", "h":
		if s.thinkIdx > 0 {
			s.thinkIdx--
		}
		return true, nil

	case "right", "l":
		if s.thinkIdx < len(s.thinkLevels)-1 {
			s.thinkIdx++
		}
		return true, nil

	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(msg.Runes[0] - '1')
		if idx < len(s.entries) {
			s.cursor = idx
			c.refreshThinking()
		}
		return true, nil

	case "enter":
		entry := s.entries[s.cursor]
		thinkLevel := ""
		if s.thinkIdx < len(s.thinkLevels) {
			thinkLevel = s.thinkLevels[s.thinkIdx]
		}

		c.app.registry.ClearOverlay()

		if err := c.app.Session.SetModel(entry.provider, entry.model); err != nil {
			return true, tui.SendCommandResult(tui.ErrorStyle.Render("Failed to switch model: " + err.Error()))
		}

		if thinkLevel != "" && thinkLevel != "off" {
			c.app.Session.SetThinkingLevel(agentcore.ThinkingLevel(thinkLevel))
		}

		display := config.FormatModelID(entry.provider, entry.model)
		if thinkLevel != "" && thinkLevel != "off" {
			display += " (thinking: " + thinkLevel + ")"
		}
		return true, func() tea.Msg {
			return tui.CommandResultMsg{
				Text:     tui.CommandStyle.Render("Switched to model: " + display),
				NewModel: entry.model,
			}
		}

	case "esc", "ctrl+c":
		c.app.registry.ClearOverlay()
		return true, nil
	}

	return true, nil
}

func (c *ModelCommand) View(width int) string {
	if c.state == nil {
		return ""
	}
	s := c.state

	var sb strings.Builder
	hint := tui.MutedStyle.Render("Select model (↑↓ navigate · ←→ thinking · Enter select · Esc cancel):")
	sb.WriteString(hint)
	sb.WriteString("\n")

	selectedStyle := lipgloss.NewStyle().Foreground(tui.ColorAccent).Bold(true)
	currentMark := lipgloss.NewStyle().Foreground(tui.ColorSuccess)
	dimStyle := tui.MutedStyle
	headerStyle := lipgloss.NewStyle().Foreground(tui.ColorAccent)

	reg := c.app.Session.Registry()

	groupIdx := 0
	for i, e := range s.entries {
		// Render group header when entering a new provider section.
		if groupIdx < len(s.groups) && s.groups[groupIdx].startIdx == i {
			g := s.groups[groupIdx]
			header := fmt.Sprintf("─ %s ", g.name) + strings.Repeat("─", max(0, 30-len(g.name)))
			sb.WriteString(headerStyle.Render(header))
			sb.WriteString("\n")
			groupIdx++
		}

		// Number prefix.
		num := fmt.Sprintf("%d.", i+1)

		// Cursor indicator.
		prefix := "  "
		if i == s.cursor {
			prefix = "> "
		}

		// Current model marker.
		marker := "  "
		isCurrent := e.provider == s.currentProv && strings.EqualFold(e.model, s.current)
		if isCurrent {
			marker = "* "
		}

		// Look up model metadata from registry for display.
		var name, ctx, reasoning string
		if reg != nil {
			if entry, _, err := reg.Resolve(e.model); err == nil {
				name = entry.Name
				ctx = tui.FormatTokens(entry.ContextWindow)
				if entry.Reasoning {
					reasoning = "reasoning"
				}
			}
		}

		// Thinking indicator for selected row.
		if i == s.cursor && len(s.thinkLevels) > 1 {
			reasoning = c.renderThinkingIndicator()
		}

		line := fmt.Sprintf("%s%s%-2s %-30s %-18s %6s  %s",
			prefix, marker, num, e.model, name, ctx, reasoning)

		if i == s.cursor {
			sb.WriteString(selectedStyle.Render(line))
		} else if isCurrent {
			sb.WriteString(currentMark.Render(line))
		} else {
			sb.WriteString(dimStyle.Render(line))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func (c *ModelCommand) Dismiss() {
	c.state = nil
}

// refreshThinking updates thinking levels when cursor moves to a new model.
func (c *ModelCommand) refreshThinking() {
	s := c.state
	reg := c.app.Session.Registry()
	if reg == nil {
		return
	}
	s.thinkLevels = reg.AvailableThinkingLevels(s.entries[s.cursor].model)
	s.thinkIdx = currentThinkingIndex(c.app, s.thinkLevels)
}

// renderThinkingIndicator shows the thinking level with ◀▶ arrows.
func (c *ModelCommand) renderThinkingIndicator() string {
	s := c.state
	if len(s.thinkLevels) == 0 {
		return ""
	}
	level := s.thinkLevels[s.thinkIdx]
	left := " "
	right := " "
	if s.thinkIdx > 0 {
		left = "◀"
	}
	if s.thinkIdx < len(s.thinkLevels)-1 {
		right = "▶"
	}
	return fmt.Sprintf("[%s %s %s]", left, level, right)
}

// currentThinkingIndex returns the index of the current thinking level in levels.
func currentThinkingIndex(app *App, levels []string) int {
	current := app.Session.Settings().ThinkingLevel
	if current == "" {
		current = "off"
	}
	for i, l := range levels {
		if l == current {
			return i
		}
	}
	return 0
}

// buildModelList constructs a flat entries list and group metadata from providers config.
func buildModelList(providers map[string]config.ProviderConfig) ([]modelSelectEntry, []modelGroup) {
	// Sort provider names for stable ordering.
	names := make([]string, 0, len(providers))
	for name, pc := range providers {
		if len(pc.Models) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var entries []modelSelectEntry
	var groups []modelGroup
	for _, name := range names {
		pc := providers[name]
		g := modelGroup{name: name, startIdx: len(entries), count: len(pc.Models)}
		for _, m := range pc.Models {
			entries = append(entries, modelSelectEntry{provider: name, model: m})
		}
		groups = append(groups, g)
	}
	return entries, groups
}

