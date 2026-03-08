package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/ui/tui"
)

// ModelCommand implements InteractiveCommand for /model.
// Without arguments it opens an interactive selector overlay;
// with arguments it switches directly.
type ModelCommand struct {
	app   *App
	state *modelSelectState // non-nil when interactive overlay is active
}

type modelSelectState struct {
	models      []provider.ModelEntry
	current     string   // current model ID for highlighting
	cursor      int      // selected row
	thinkLevels []string // available thinking levels for cursor model
	thinkIdx    int      // current thinking level index
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

	// Open interactive selector — only show current provider's models.
	reg := ctx.App.Session.Registry()
	if reg == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("No model registry available."))
	}
	prov := ctx.App.Session.Provider()
	models := reg.FindByProvider(prov)
	if len(models) == 0 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("No models available for provider: " + prov))
	}

	current := ctx.App.Session.ModelName()
	cursor := 0
	for i, m := range models {
		if strings.EqualFold(m.ID, current) {
			cursor = i
			break
		}
	}

	thinkLevels := reg.AvailableThinkingLevels(models[cursor].ID)
	thinkIdx := currentThinkingIndex(ctx.App, thinkLevels)

	c.state = &modelSelectState{
		models:      models,
		current:     current,
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
		if s.cursor < len(s.models)-1 {
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
		if idx < len(s.models) {
			s.cursor = idx
			c.refreshThinking()
		}
		return true, nil

	case "enter":
		selected := s.models[s.cursor]
		thinkLevel := ""
		if s.thinkIdx < len(s.thinkLevels) {
			thinkLevel = s.thinkLevels[s.thinkIdx]
		}

		pattern := selected.ID
		if thinkLevel != "" && thinkLevel != "off" {
			pattern += ":" + thinkLevel
		}

		c.app.registry.ClearOverlay() // safe: local vars already captured from state

		resolved, err := c.app.Session.ResolveAndSetModel(pattern)
		if err != nil {
			return true, tui.SendCommandResult(tui.ErrorStyle.Render("Failed to switch model: " + err.Error()))
		}

		display := resolved
		if thinkLevel != "" && thinkLevel != "off" {
			display += " (thinking: " + thinkLevel + ")"
		}
		return true, func() tea.Msg {
			return tui.CommandResultMsg{
				Text:     tui.CommandStyle.Render("Switched to model: " + display),
				NewModel: resolved,
			}
		}

	case "esc", "ctrl+c":
		c.app.registry.ClearOverlay()
		return true, nil
	}

	return true, nil // swallow other keys while overlay is active
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

	for i, m := range s.models {
		// Number prefix.
		num := fmt.Sprintf("%d.", i+1)

		// Cursor indicator.
		prefix := "  "
		if i == s.cursor {
			prefix = "> "
		}

		// Current model marker.
		marker := "  "
		if strings.EqualFold(m.ID, s.current) {
			marker = "* "
		}

		// Model info.
		ctx := tui.FormatTokens(m.ContextWindow)

		var reasoning string
		if m.Reasoning {
			reasoning = "reasoning"
			if i == s.cursor && len(s.thinkLevels) > 1 {
				reasoning = c.renderThinkingIndicator()
			}
		}

		line := fmt.Sprintf("%s%s%-2s %-30s %-18s %6s  %s",
			prefix, marker, num, m.ID, m.Name, ctx, reasoning)

		if i == s.cursor {
			sb.WriteString(selectedStyle.Render(line))
		} else if strings.EqualFold(m.ID, s.current) {
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
	s.thinkLevels = reg.AvailableThinkingLevels(s.models[s.cursor].ID)
	// Reset to current level or clamp.
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
