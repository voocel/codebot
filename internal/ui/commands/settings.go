package commands

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/ui/tui"
)

// SettingsCommand drives /settings — a tabbed modal overlay reporting the
// current general / runtime / providers configuration.
type SettingsCommand struct {
	session  *agent.Session
	overlay  OverlayController
	approval *approval.Engine
	cwd      string

	state *settingsState
}

type settingsState struct {
	active int
}

var settingsTabs = []string{"general", "runtime", "providers"}

// Settings constructs the /settings command.
func Settings(session *agent.Session, overlay OverlayController, approvalEngine *approval.Engine, cwd string) *SettingsCommand {
	return &SettingsCommand{
		session:  session,
		overlay:  overlay,
		approval: approvalEngine,
		cwd:      cwd,
	}
}

func (c *SettingsCommand) Spec() Spec {
	return Spec{
		Name:        "settings",
		Usage:       "/settings",
		Description: "Show current settings",
		Category:    "info",
		Kind:        KindBuiltin,
	}
}

func (c *SettingsCommand) Run(_ Invocation) tea.Cmd {
	c.state = &settingsState{active: 0}
	c.overlay.SetOverlay(c)
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
		c.overlay.ClearOverlay()
		return true, nil
	}
	return true, nil
}

func (c *SettingsCommand) View(width, height int) string {
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
		Height: height,
	}
	return frame.Render()
}

func (c *SettingsCommand) renderGeneral(width int) string {
	s := c.session.Settings()
	baseURL := c.session.BaseURL()
	if baseURL == "" {
		baseURL = "(default)"
	}
	p := newPanel(width)
	p.Row("Provider", s.Provider)
	p.Row("Model", c.session.ModelName())
	p.Row("API Key", maskAPIKey(c.session.APIKey()))
	p.Row("Base URL", baseURL)
	p.Hint("Config", config.SettingsPath(c.cwd))
	return p.Render()
}

func (c *SettingsCommand) renderRuntime(width int) string {
	s := c.session.Settings()
	effort := s.ReasoningEffort
	if effort == "" {
		effort = "(unset)"
	}

	mode := "(unset)"
	if c.approval != nil {
		if c.approval.PlanMode() {
			mode = "plan"
		} else {
			mode = formatApprovalMode(c.approval.Mode())
		}
	}

	p := newPanel(width)
	p.Row("Reasoning Effort", effort)
	p.Row("Context", tui.FormatTokens(s.ContextWindow))
	if s.CompactWindow > 0 {
		p.Hint("Compact Cap", tui.FormatTokens(s.CompactWindow))
	}
	if s.CompactRatio > 0 {
		p.Hint("Compact At", fmt.Sprintf("%.0f%%", s.CompactRatio*100))
	}
	p.Row("Max Turns", fmt.Sprintf("%d", s.MaxTurns))
	p.Row("Mode", mode)
	if s.SmallModel != "" && s.SmallModel != c.session.ModelName() {
		p.Hint("SubAgent", s.SmallModel)
	}
	return p.Render()
}

func (c *SettingsCommand) renderProviders(width int) string {
	s := c.session.Settings()
	if len(s.Providers) == 0 {
		return tui.MutedStyle.Render("  No providers configured.")
	}

	names := make([]string, 0, len(s.Providers))
	for n := range s.Providers {
		names = append(names, n)
	}
	sort.Strings(names)

	current := s.Provider
	p := newPanel(width)
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

func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func formatApprovalMode(m approval.Mode) string {
	switch m {
	case approval.ModeStrict:
		return "strict"
	case approval.ModeBalanced:
		return "balanced"
	case approval.ModeAuto:
		return "auto"
	case approval.ModeTrust:
		return "trust"
	default:
		return string(m)
	}
}
