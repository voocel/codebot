package ui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/tools"
	"github.com/voocel/codebot/internal/ui/tui"
)

type builtinCommandLoader struct{}

func (builtinCommandLoader) Load(app *App) []Command {
	return app.builtinCommands()
}

type fileCommandLoader struct{}

func (fileCommandLoader) Load(app *App) []Command {
	commands := make([]Command, 0, len(app.Commands))
	for _, cmd := range app.Commands {
		commands = append(commands, &FileCommandAdapter{command: cmd})
	}
	return commands
}

type skillCommandLoader struct{}

func (skillCommandLoader) Load(app *App) []Command {
	commands := make([]Command, 0, len(app.Skills))
	for _, skill := range app.Skills {
		if skill.DisableUserInvocation {
			continue
		}
		commands = append(commands, &SkillCommand{skill: skill})
	}
	return commands
}

func (a *App) commandLoaders() []CommandLoader {
	if len(a.CommandLoaders) > 0 {
		return a.CommandLoaders
	}
	return []CommandLoader{
		skillCommandLoader{},
		fileCommandLoader{},
		builtinCommandLoader{},
	}
}

func (a *App) rebuildRegistry() {
	reg := NewRegistry()
	for _, loader := range a.commandLoaders() {
		reg.RegisterAll(loader.Load(a))
	}
	a.registry = reg
}

type FileCommandAdapter struct {
	command config.FileCommand
}

func (c *FileCommandAdapter) Spec() CommandSpec {
	return CommandSpec{
		Name:        c.command.Name,
		Aliases:     c.command.Aliases,
		Usage:       c.command.Usage,
		Description: c.command.Description,
		Category:    c.command.Category,
		NeedsIdle:   c.command.NeedsIdle,
		Hidden:      c.command.Hidden,
		Kind:        CommandKindCustom,
		Source:      c.command.Source,
	}
}

func (c *FileCommandAdapter) Run(ctx *CommandContext, inv CommandInvocation) tea.Cmd {
	expanded := Expand(c.command.Content, inv.Args)
	return ctx.App.sendAsPrompt(expanded)
}

type SkillCommand struct {
	skill config.Skill
}

func (c *SkillCommand) Spec() CommandSpec {
	usage := "/" + c.skill.Name + " [args]"
	if c.skill.ArgumentHint != "" {
		usage = "/" + c.skill.Name + " " + c.skill.ArgumentHint
	}
	return CommandSpec{
		Name:        c.skill.Name,
		Usage:       usage,
		Description: c.skill.Description,
		Category:    "prompt",
		Kind:        CommandKindSkill,
		Source:      c.skill.Source,
	}
}

func (c *SkillCommand) Run(ctx *CommandContext, inv CommandInvocation) tea.Cmd {
	// context: fork — route through the Skill tool so fork/allowed-tools/model are handled.
	// Inline skills with allowed-tools also route through the Skill tool for consistency.
	if c.skill.Context == "fork" || len(c.skill.AllowedTools) > 0 {
		return ctx.App.sendAsPrompt(fmt.Sprintf(
			"Invoke the Skill tool: skill=%q args=%q", c.skill.Name, inv.RawArgs))
	}

	data, err := os.ReadFile(c.skill.FilePath)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			fmt.Sprintf("Failed to read skill file: %v", err)))
	}

	body := strings.TrimSpace(config.StripFrontmatter(string(data)))
	body = tools.ExpandSkillVars(body, c.skill.BaseDir, ctx.App.Session.SessionID())
	body = tools.ExpandShellInjections(body)
	body = tools.ExpandSkillArgs(body, inv.RawArgs)

	var sb strings.Builder
	fmt.Fprintf(&sb, "<skill name=%q>\n", c.skill.Name)
	fmt.Fprintf(&sb, "References are relative to %s.\n\n", c.skill.BaseDir)
	sb.WriteString(body)
	sb.WriteString("\n</skill>")

	return ctx.App.sendAsPrompt(sb.String())
}
