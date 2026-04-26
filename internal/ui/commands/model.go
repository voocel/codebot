package commands

import (
	"fmt"
	"os"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/ui/tui"
)

// ModelCommand drives /model — an interactive selector that switches the
// current chat model and persists the choice to whichever settings file
// already owns the model setting (project if present, otherwise global).
type ModelCommand struct {
	session *agent.Session
	overlay OverlayController
	cwd     string

	state *modelSelectState
}

type modelSelectEntry struct {
	provider string
	model    string
}

type modelGroup struct {
	name     string
	startIdx int
	count    int
}

type modelSelectState struct {
	entries     []modelSelectEntry
	groups      []modelGroup
	current     string
	currentProv string
	cursor      int
	thinkLevels []string
	thinkIdx    int
}

// Model constructs the /model command.
func Model(session *agent.Session, overlay OverlayController, cwd string) *ModelCommand {
	return &ModelCommand{session: session, overlay: overlay, cwd: cwd}
}

func (c *ModelCommand) Spec() Spec {
	return Spec{
		Name:        "model",
		Aliases:     []string{"m"},
		Usage:       "/model",
		Description: "Show or switch model",
		Category:    "config",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}
}

func (c *ModelCommand) Run(_ Invocation) tea.Cmd {
	settings := c.session.Settings()
	entries, groups := buildModelList(settings.Providers)
	if len(entries) == 0 {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			"No models configured. Add models to your providers in .codebot/settings.json"))
	}

	currentModel := c.session.ModelName()
	currentProv := c.session.Provider()

	cursor := 0
	for i, e := range entries {
		if e.provider == currentProv && strings.EqualFold(e.model, currentModel) {
			cursor = i
			break
		}
	}

	reg := c.session.Registry()
	var thinkLevels []string
	if reg != nil {
		thinkLevels = reg.AvailableThinkingLevels(entries[cursor].model)
	} else {
		thinkLevels = []string{"off"}
	}
	thinkIdx := currentThinkingIndex(c.session, thinkLevels)

	c.state = &modelSelectState{
		entries:     entries,
		groups:      groups,
		current:     currentModel,
		currentProv: currentProv,
		cursor:      cursor,
		thinkLevels: thinkLevels,
		thinkIdx:    thinkIdx,
	}
	c.overlay.SetOverlay(c)
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

		c.overlay.ClearOverlay()

		if err := c.session.SetModel(entry.provider, entry.model); err != nil {
			return true, tui.SendCommandResult(tui.ErrorStyle.Render("Failed to switch model: " + err.Error()))
		}

		if thinkLevel != "" && thinkLevel != "off" {
			c.session.SetThinkingLevel(agentcore.ThinkingLevel(thinkLevel))
		}

		// Persist selection so manual edits and /model share one source of
		// truth. SmallModel is written alongside to avoid leaving a stale
		// value from a previous provider.
		prov := entry.provider
		model := entry.model
		small := c.session.Settings().SmallModel
		patch := config.Settings{
			Provider:   &prov,
			Model:      &model,
			SmallModel: &small,
		}
		var perr error
		if config.ProjectSettingsDefinesModel(c.cwd) {
			perr = config.PatchProjectSettings(c.cwd, patch)
		} else {
			perr = config.PatchGlobalSettings(patch)
		}
		if perr != nil {
			fmt.Fprintf(os.Stderr, "warning: persist model setting: %v\n", perr)
		}

		display := config.FormatModelID(entry.provider, entry.model)
		if thinkLevel != "" && thinkLevel != "off" {
			display += " (thinking: " + thinkLevel + ")"
		}
		return true, func() tea.Msg {
			return tui.CommandResultMsg{
				Text:             tui.SystemMsgStyle.Render("Switched to model: " + display),
				NewProvider:      entry.provider,
				NewModel:         entry.model,
				NewContextWindow: c.session.Settings().ContextWindow,
			}
		}

	case "esc", "ctrl+c":
		c.overlay.ClearOverlay()
		return true, nil
	}

	return true, nil
}

func (c *ModelCommand) View(width, _ int) string {
	if c.state == nil {
		return ""
	}
	s := c.state

	var sb strings.Builder
	hint := tui.MutedStyle.Render("Select model (↑↓ navigate · ←→ thinking · Enter select · Esc cancel):")
	sb.WriteString(hint)
	sb.WriteString("\n")

	selectedStyle := lipgloss.NewStyle().Foreground(tui.Brand).Bold(true)
	currentMark := lipgloss.NewStyle().Foreground(tui.Success)
	dimStyle := tui.MutedStyle
	headerStyle := tui.MutedStyle

	reg := c.session.Registry()

	groupIdx := 0
	for i, e := range s.entries {
		if groupIdx < len(s.groups) && s.groups[groupIdx].startIdx == i {
			g := s.groups[groupIdx]
			sb.WriteString(headerStyle.Render("  " + g.name))
			sb.WriteString("\n")
			groupIdx++
		}

		num := fmt.Sprintf("%d.", i+1)

		prefix := "  "
		if i == s.cursor {
			prefix = "> "
		}

		marker := "  "
		isCurrent := e.provider == s.currentProv && strings.EqualFold(e.model, s.current)
		if isCurrent {
			marker = "* "
		}

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

	_ = width
	return sb.String()
}

func (c *ModelCommand) Dismiss() {
	c.state = nil
}

func (c *ModelCommand) refreshThinking() {
	s := c.state
	reg := c.session.Registry()
	if reg == nil {
		return
	}
	s.thinkLevels = reg.AvailableThinkingLevels(s.entries[s.cursor].model)
	s.thinkIdx = currentThinkingIndex(c.session, s.thinkLevels)
}

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

func currentThinkingIndex(session *agent.Session, levels []string) int {
	current := session.Settings().ThinkingLevel
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

func buildModelList(providers map[string]config.ProviderConfig) ([]modelSelectEntry, []modelGroup) {
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
