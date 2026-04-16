package ui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/ui/tui"
)

// SettingsCommand implements InteractiveCommand for /settings.
// Opens a modal overlay with tabbed sections (general / runtime / providers).
type SettingsCommand struct {
	app   *App
	state *settingsState
}

type settingsState struct {
	active int
}

var settingsTabs = []string{"general", "runtime", "providers"}

func NewSettingsCommand(app *App) *SettingsCommand {
	return &SettingsCommand{app: app}
}

func (c *SettingsCommand) Spec() CommandSpec {
	return CommandSpec{
		Name:        "settings",
		Usage:       "/settings",
		Description: "Show current settings",
		Category:    "info",
		Kind:        CommandKindBuiltin,
	}
}

func (c *SettingsCommand) Run(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
	c.state = &settingsState{active: 0}
	ctx.App.registry.SetOverlay(c)
	return nil
}

func (c *SettingsCommand) Active() bool  { return c.state != nil }
func (c *SettingsCommand) IsModal() bool { return true }
func (c *SettingsCommand) Dismiss()      { c.state = nil }

func (c *SettingsCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if c.state == nil {
		return false, nil
	}
	switch msg.String() {
	case "tab", "right", "l":
		c.state.active = (c.state.active + 1) % len(settingsTabs)
		return true, nil
	case "shift+tab", "left", "h":
		c.state.active = (c.state.active - 1 + len(settingsTabs)) % len(settingsTabs)
		return true, nil
	case "1", "2", "3":
		idx := int(msg.Runes[0] - '1')
		if idx < len(settingsTabs) {
			c.state.active = idx
		}
		return true, nil
	case "esc", "ctrl+c", "q":
		c.app.registry.ClearOverlay()
		return true, nil
	}
	return true, nil
}

func (c *SettingsCommand) View(width int) string {
	if c.state == nil {
		return ""
	}
	frame := tui.InfoOverlayFrame{
		Title: "Settings",
		Tabs: []tui.InfoOverlayTab{
			{Name: settingsTabs[0], Body: c.renderGeneral},
			{Name: settingsTabs[1], Body: c.renderRuntime},
			{Name: settingsTabs[2], Body: c.renderProviders},
		},
		Active: c.state.active,
		Hint:   "Tab / ←→ switch · 1-3 jump · Esc close",
		Width:  width,
	}
	return frame.Render()
}

func (c *SettingsCommand) renderGeneral() string {
	s := c.app.Session.Settings()
	baseURL := c.app.Session.BaseURL()
	if baseURL == "" {
		baseURL = "(default)"
	}
	p := tui.NewInfoPanel("")
	p.Row("Provider", s.Provider)
	p.Row("Model", c.app.Session.ModelName())
	p.Row("API Key", maskKey(c.app.Session.APIKey()))
	p.Row("Base URL", baseURL)
	p.Hint("Config", config.SettingsPath(c.app.Cwd))
	return p.Render()
}

func (c *SettingsCommand) renderRuntime() string {
	s := c.app.Session.Settings()
	thinking := s.ThinkingLevel
	if thinking == "" {
		thinking = "(unset)"
	}

	mode := "(unset)"
	if c.app.ApprovalEngine != nil {
		if c.app.ApprovalEngine.PlanMode() {
			mode = "plan"
		} else {
			mode = formatApprovalMode(c.app.ApprovalEngine.Mode())
		}
	}

	p := tui.NewInfoPanel("")
	p.Row("Thinking", thinking)
	p.Row("Context", tui.FormatTokens(s.ContextWindow))
	if s.CompactWindow > 0 {
		p.Hint("Compact Cap", tui.FormatTokens(s.CompactWindow))
	}
	if s.CompactRatio > 0 {
		p.Hint("Compact At", fmt.Sprintf("%.0f%%", s.CompactRatio*100))
	}
	p.Row("Max Turns", fmt.Sprintf("%d", s.MaxTurns))
	p.Row("Mode", mode)
	if s.SmallModel != "" && s.SmallModel != c.app.Session.ModelName() {
		p.Hint("SubAgent", s.SmallModel)
	}
	return p.Render()
}

func (c *SettingsCommand) renderProviders() string {
	s := c.app.Session.Settings()
	if len(s.Providers) == 0 {
		return tui.MutedStyle.Render("  No providers configured.")
	}

	names := make([]string, 0, len(s.Providers))
	for n := range s.Providers {
		names = append(names, n)
	}
	sort.Strings(names)

	current := s.Provider
	p := tui.NewInfoPanel("")
	for _, n := range names {
		pc := s.Providers[n]
		parts := []string{fmt.Sprintf("%d model(s)", len(pc.Models))}
		if pc.BaseURL != "" {
			parts = append(parts, pc.BaseURL)
		}
		marker := "  "
		if n == current {
			marker = "* "
		}
		p.Row(marker+n, strings.Join(parts, " · "))
	}
	return p.Render()
}

func formatApprovalMode(m approval.Mode) string {
	switch m {
	case approval.ModeStrict:
		return "strict"
	case approval.ModeBalanced:
		return "balanced"
	case approval.ModeAcceptEdits:
		return "accept-edits"
	case approval.ModeTrust:
		return "trust"
	default:
		return string(m)
	}
}
