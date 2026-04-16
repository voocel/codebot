package ui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/codebot/internal/ui/tui"
)

// HelpCommand implements InteractiveCommand for /help.
// Opens a modal overlay with tabbed sections (general / built-in / custom / skills).
type HelpCommand struct {
	app   *App
	state *helpState
}

type helpState struct {
	active int
}

var helpTabs = []string{"general", "built-in", "custom", "skills"}

func NewHelpCommand(app *App) *HelpCommand {
	return &HelpCommand{app: app}
}

func (c *HelpCommand) Spec() CommandSpec {
	return CommandSpec{
		Name:        "help",
		Usage:       "/help",
		Description: "Show this help",
		Category:    "info",
		Kind:        CommandKindBuiltin,
	}
}

func (c *HelpCommand) Run(ctx *CommandContext, _ CommandInvocation) tea.Cmd {
	c.state = &helpState{active: 0}
	ctx.App.registry.SetOverlay(c)
	return nil
}

func (c *HelpCommand) Active() bool  { return c.state != nil }
func (c *HelpCommand) IsModal() bool { return true }
func (c *HelpCommand) Dismiss()      { c.state = nil }

func (c *HelpCommand) HandleKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if c.state == nil {
		return false, nil
	}
	switch msg.String() {
	case "tab", "right", "l":
		c.state.active = (c.state.active + 1) % len(helpTabs)
		return true, nil
	case "shift+tab", "left", "h":
		c.state.active = (c.state.active - 1 + len(helpTabs)) % len(helpTabs)
		return true, nil
	case "1", "2", "3", "4":
		idx := int(msg.Runes[0] - '1')
		if idx < len(helpTabs) {
			c.state.active = idx
		}
		return true, nil
	case "esc", "ctrl+c", "q":
		c.app.registry.ClearOverlay()
		return true, nil
	}
	return true, nil
}

func (c *HelpCommand) View(width int) string {
	if c.state == nil {
		return ""
	}
	frame := tui.InfoOverlayFrame{
		Title: "Codebot",
		Tabs: []tui.InfoOverlayTab{
			{Name: helpTabs[0], Body: c.renderGeneral},
			{Name: helpTabs[1], Body: c.renderBuiltin},
			{Name: helpTabs[2], Body: c.renderCustom},
			{Name: helpTabs[3], Body: c.renderSkills},
		},
		Active: c.state.active,
		Hint:   "Tab / ←→ switch · 1-4 jump · Esc close",
		Width:  width,
	}
	return frame.Render()
}

func (c *HelpCommand) renderGeneral() string {
	labelStyle := lipgloss.NewStyle().Foreground(tui.ColorMuted)
	valueStyle := lipgloss.NewStyle().Foreground(tui.ColorSoftText)
	sectionStyle := lipgloss.NewStyle().Foreground(tui.ColorPrimarySoft).Bold(true)

	var sb strings.Builder
	sb.WriteString("  ")
	sb.WriteString(valueStyle.Render("Terminal-native AI coding agent. " +
		"Understands your codebase, makes edits with your permission, and runs commands."))
	sb.WriteString("\n\n")

	row := func(k, v string) {
		sb.WriteString(labelStyle.Render(fmt.Sprintf("  %-18s", k)))
		sb.WriteString(valueStyle.Render(v))
		sb.WriteString("\n")
	}

	sb.WriteString(sectionStyle.Render("  Basics"))
	sb.WriteString("\n")
	row("/", "browse slash commands")
	row("!<cmd>", "run a shell command inline")
	row("Ctrl+V", "paste image from clipboard")
	row("Drop file", "attach an image by dragging it in")
	sb.WriteString("\n")

	sb.WriteString(sectionStyle.Render("  Shortcuts"))
	sb.WriteString("\n")
	row("Enter", "send message")
	row("Ctrl+J / Alt+Enter", "newline in input")
	row("Esc", "abort running agent")
	row("Ctrl+C", "quit (press twice to confirm)")
	row("Shift+Tab", "cycle permission mode")
	row("Up / Down", "navigate input history")
	row("Tab", "accept completion / suggestion")

	return strings.TrimRight(sb.String(), "\n")
}

func (c *HelpCommand) renderBuiltin() string {
	return c.renderCommandGroup(CommandKindBuiltin,
		"No built-in commands registered.")
}

func (c *HelpCommand) renderCustom() string {
	return c.renderCommandGroup(CommandKindCustom,
		"No custom commands. Add Markdown files under .codebot/commands/ to register slash commands.")
}

func (c *HelpCommand) renderSkills() string {
	return c.renderCommandGroup(CommandKindSkill,
		"No skills loaded. Drop skill bundles into .codebot/skills/ or install via /plugins install.")
}

func (c *HelpCommand) renderCommandGroup(kind CommandKind, emptyMsg string) string {
	var cmds []Command
	for _, cmd := range c.app.registry.All() {
		spec := c.app.registry.EffectiveSpec(cmd)
		if spec.Hidden || spec.Kind != kind {
			continue
		}
		cmds = append(cmds, cmd)
	}
	if len(cmds) == 0 {
		return tui.MutedStyle.Render("  " + emptyMsg)
	}

	sort.SliceStable(cmds, func(i, j int) bool {
		return cmds[i].Spec().Name < cmds[j].Spec().Name
	})

	usageStyle := lipgloss.NewStyle().Foreground(tui.ColorCommand)
	descStyle := lipgloss.NewStyle().Foreground(tui.ColorSoftText)
	metaStyle := lipgloss.NewStyle().Foreground(tui.ColorMuted)

	var sb strings.Builder
	for _, cmd := range cmds {
		spec := c.app.registry.EffectiveSpec(cmd)

		usage := spec.Usage
		if usage == "" {
			usage = "/" + spec.Name
		}
		desc := spec.Description
		if desc == "" {
			desc = "(no description)"
		}

		var tags []string
		if spec.NeedsIdle {
			tags = append(tags, "idle")
		}
		if len(spec.Aliases) > 0 {
			tags = append(tags, "aliases: "+strings.Join(spec.Aliases, ","))
		}
		if spec.Source != "" {
			tags = append(tags, spec.Source)
		}

		sb.WriteString("  ")
		sb.WriteString(usageStyle.Render(fmt.Sprintf("%-22s", usage)))
		sb.WriteString(" ")
		sb.WriteString(descStyle.Render(desc))
		if len(tags) > 0 {
			sb.WriteString(" ")
			sb.WriteString(metaStyle.Render("[" + strings.Join(tags, " · ") + "]"))
		}
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}
