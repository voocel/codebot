package commands

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/ui/tui"
)

// ModelCommand drives /model — an interactive selector that switches the
// conversation's model and reasoning effort, remembered in the settings.
type ModelCommand struct {
	app     *app.App
	overlay OverlayController

	state *modelSelectState
}

// tabsWindowSize caps how many provider tabs render at once. When more
// providers exist, ←/→ scrolls the window so the active tab stays visible.
const tabsWindowSize = 6

type providerSection struct {
	name   string
	models []string
}

type modelSelectState struct {
	sections     []providerSection
	provIdx      int // index into sections
	provWinStart int // first visible tab; window is [start, start+tabsWindowSize)
	modelIdx     int // index within sections[provIdx].models
	current      string
	currentProv  string
	effort       string // the conversation's reasoning effort
	thinkLevels  []string
	thinkIdx     int
}

// Model constructs the /model command.
func Model(a *app.App, overlay OverlayController) *ModelCommand {
	return &ModelCommand{app: a, overlay: overlay}
}

func (c *ModelCommand) Spec() Spec {
	return Spec{
		Name:        "model",
		Aliases:     []string{"m"},
		Usage:       "/model",
		Description: "Show or switch model",
		NeedsIdle:   true,
		Kind:        KindBuiltin,
	}
}

func (c *ModelCommand) Run(_ Invocation) tea.Cmd {
	sections := buildProviderSections(c.app.Settings().Providers)
	if len(sections) == 0 {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			"No models configured. Add models to your providers in .codebot/settings.json"))
	}

	st := c.app.Current().Status()
	currentModel, currentProv := st.Model, st.Provider

	provIdx, modelIdx := 0, 0
	for i, s := range sections {
		if s.name == currentProv {
			provIdx = i
			for j, m := range s.models {
				if strings.EqualFold(m, currentModel) {
					modelIdx = j
					break
				}
			}
			break
		}
	}

	c.state = &modelSelectState{
		sections:     sections,
		provIdx:      provIdx,
		provWinStart: clampWindowStart(provIdx, 0, len(sections), tabsWindowSize),
		modelIdx:     modelIdx,
		current:      currentModel,
		currentProv:  currentProv,
		effort:       string(st.Effort),
	}
	c.refreshThinking()
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
		if s.modelIdx > 0 {
			s.modelIdx--
			c.refreshThinking()
		}
		return true, nil

	case "down", "j":
		if s.modelIdx < len(s.sections[s.provIdx].models)-1 {
			s.modelIdx++
			c.refreshThinking()
		}
		return true, nil

	case "left", "h":
		if s.provIdx > 0 {
			s.provIdx--
			s.modelIdx = 0
			s.provWinStart = clampWindowStart(s.provIdx, s.provWinStart, len(s.sections), tabsWindowSize)
			c.refreshThinking()
		}
		return true, nil

	case "right", "l":
		if s.provIdx < len(s.sections)-1 {
			s.provIdx++
			s.modelIdx = 0
			s.provWinStart = clampWindowStart(s.provIdx, s.provWinStart, len(s.sections), tabsWindowSize)
			c.refreshThinking()
		}
		return true, nil

	case "tab":
		if len(s.thinkLevels) > 1 {
			s.thinkIdx = (s.thinkIdx + 1) % len(s.thinkLevels)
		}
		return true, nil

	case "shift+tab":
		if len(s.thinkLevels) > 1 {
			s.thinkIdx = (s.thinkIdx - 1 + len(s.thinkLevels)) % len(s.thinkLevels)
		}
		return true, nil

	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(msg.Runes[0] - '1')
		if idx < len(s.sections[s.provIdx].models) {
			s.modelIdx = idx
			c.refreshThinking()
		}
		return true, nil

	case "enter":
		section := s.sections[s.provIdx]
		prov := section.name
		model := section.models[s.modelIdx]
		thinkLevel := ""
		if s.thinkIdx < len(s.thinkLevels) {
			thinkLevel = s.thinkLevels[s.thinkIdx]
		}

		c.overlay.ClearOverlay()

		if err := c.app.Current().SetModel(prov, model, thinkLevel); err != nil {
			return true, tui.SendCommandResult(tui.ErrorStyle.Render("Failed to switch model: " + err.Error()))
		}
		display := config.FormatModelID(prov, model)
		if thinkLevel != "" && thinkLevel != "off" {
			display += " (reasoning_effort: " + thinkLevel + ")"
		}
		return true, tui.SendCommandResult(tui.SystemMsgStyle.Render("Switched to model: " + display))

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
	hint := tui.MutedStyle.Render("Select model (↑↓ select · ←→ provider · Tab effort · Enter confirm · Esc cancel):")
	sb.WriteString(hint)
	sb.WriteString("\n\n")

	sb.WriteString(c.renderTabBar())
	sb.WriteString("\n\n")

	selectedStyle := lipgloss.NewStyle().Foreground(tui.Brand).Bold(true)
	currentMark := lipgloss.NewStyle().Foreground(tui.Success)
	dimStyle := tui.MutedStyle

	models := s.sections[s.provIdx].models
	provName := s.sections[s.provIdx].name

	for i, m := range models {
		num := fmt.Sprintf("%d.", i+1)

		prefix := "  "
		if i == s.modelIdx {
			prefix = "> "
		}

		marker := "  "
		isCurrent := provName == s.currentProv && strings.EqualFold(m, s.current)
		if isCurrent {
			marker = "* "
		}

		var ctx, reasoning string
		if facts, ok := c.app.ModelFacts(provName, m); ok {
			if facts.MaxInputTokens > 0 {
				ctx = tui.FormatTokens(facts.MaxInputTokens)
			}
			if facts.Reasoning != nil && *facts.Reasoning {
				reasoning = "reasoning"
			}
		}

		if i == s.modelIdx && len(s.thinkLevels) > 1 {
			reasoning = c.renderThinkingIndicator()
		}

		line := fmt.Sprintf("%s%s%-2s %-30s %6s  %s",
			prefix, marker, num, m, ctx, reasoning)

		switch {
		case i == s.modelIdx:
			sb.WriteString(selectedStyle.Render(line))
		case isCurrent:
			sb.WriteString(currentMark.Render(line))
		default:
			sb.WriteString(dimStyle.Render(line))
		}
		sb.WriteString("\n")
	}

	_ = width
	return sb.String()
}

func (c *ModelCommand) renderTabBar() string {
	s := c.state
	total := len(s.sections)
	winEnd := min(s.provWinStart+tabsWindowSize, total)

	activeStyle := lipgloss.NewStyle().Foreground(tui.Brand).Bold(true).Underline(true)
	inactiveStyle := tui.MutedStyle
	arrowStyle := tui.MutedStyle

	leftArrow := "  "
	if s.provWinStart > 0 {
		leftArrow = arrowStyle.Render("◂ ")
	}
	rightArrow := "  "
	if winEnd < total {
		rightArrow = arrowStyle.Render(" ▸")
	}

	var parts []string
	for i := s.provWinStart; i < winEnd; i++ {
		name := s.sections[i].name
		if i == s.provIdx {
			parts = append(parts, activeStyle.Render(name))
		} else {
			parts = append(parts, inactiveStyle.Render(name))
		}
	}

	separator := inactiveStyle.Render(" · ")
	return "  " + leftArrow + strings.Join(parts, separator) + rightArrow
}

func (c *ModelCommand) Dismiss() {
	c.state = nil
}

func (c *ModelCommand) refreshThinking() {
	s := c.state
	section := s.sections[s.provIdx]
	model := section.models[s.modelIdx]
	s.thinkLevels = c.app.ThinkingLevels(section.name, model)
	s.thinkIdx = max(0, slices.Index(s.thinkLevels, s.effort))
}

func (c *ModelCommand) renderThinkingIndicator() string {
	s := c.state
	if len(s.thinkLevels) == 0 {
		return ""
	}
	return fmt.Sprintf("[◂ %s ▸]", thinkingLabel(s.thinkLevels[s.thinkIdx]))
}

func thinkingLabel(level string) string {
	if level == "" {
		return "auto"
	}
	return level
}

func buildProviderSections(providers map[string]config.ProviderConfig) []providerSection {
	names := make([]string, 0, len(providers))
	for name, pc := range providers {
		if len(pc.Models) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	sections := make([]providerSection, 0, len(names))
	for _, name := range names {
		pc := providers[name]
		models := append([]string(nil), pc.Models...)
		sections = append(sections, providerSection{name: name, models: models})
	}
	return sections
}

// clampWindowStart returns a window start that keeps idx within
// [start, start+size) and inside [0, total). When total <= size, the window
// always starts at 0.
func clampWindowStart(idx, start, total, size int) int {
	if total <= size {
		return 0
	}
	if idx < start {
		start = idx
	}
	if idx >= start+size {
		start = idx - size + 1
	}
	if max := total - size; start > max {
		start = max
	}
	if start < 0 {
		start = 0
	}
	return start
}
