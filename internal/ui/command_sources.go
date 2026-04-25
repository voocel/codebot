package ui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/ui/commands"
	"github.com/voocel/codebot/internal/ui/tui"
)

// CommandLoader discovers a family of commands and contributes them to the registry.
type CommandLoader interface {
	Load(app *App) []commands.Command
}

type builtinCommandLoader struct{}

func (builtinCommandLoader) Load(app *App) []commands.Command {
	return app.builtinCommands()
}

type fileCommandLoader struct{}

func (fileCommandLoader) Load(app *App) []commands.Command {
	cmds := make([]commands.Command, 0, len(app.Commands))
	for _, cmd := range app.Commands {
		cmds = append(cmds, &fileCommand{app: app, command: cmd})
	}
	return cmds
}

type skillCommandLoader struct{}

func (skillCommandLoader) Load(app *App) []commands.Command {
	skills := app.Skills
	if app.SkillCatalog != nil {
		skills = app.SkillCatalog.List()
		app.Skills = skills
	}
	cmds := make([]commands.Command, 0, len(skills))
	for _, sk := range skills {
		if sk.DisableUserInvocation {
			continue
		}
		cmds = append(cmds, &skillCommand{app: app, skill: sk})
	}
	return cmds
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

// rebuildRegistry repopulates the registry in-place. The registry pointer
// must stay stable because builtin commands capture it at construction time
// (commands.Btw / commands.Help / ... call SetOverlay on the captured value).
func (a *App) rebuildRegistry() {
	if a.registry == nil {
		a.registry = NewRegistry()
	} else {
		a.registry.Reset()
	}
	for _, loader := range a.commandLoaders() {
		a.registry.RegisterAll(loader.Load(a))
	}
}

// fileCommand adapts a config.FileCommand (markdown-backed user command) to commands.Command.
type fileCommand struct {
	app     *App
	command config.FileCommand
}

func (c *fileCommand) Spec() commands.Spec {
	return commands.Spec{
		Name:        c.command.Name,
		Aliases:     c.command.Aliases,
		Usage:       c.command.Usage,
		Description: c.command.Description,
		Category:    c.command.Category,
		NeedsIdle:   c.command.NeedsIdle,
		Hidden:      c.command.Hidden,
		Kind:        commands.KindCustom,
		Source:      c.command.Source,
	}
}

func (c *fileCommand) Run(inv commands.Invocation) tea.Cmd {
	expanded := commands.Expand(c.command.Content, inv.Args)
	return c.app.sendAsPrompt(expanded)
}

// skillCommand adapts a skill.Spec to commands.Command.
type skillCommand struct {
	app   *App
	skill skill.Spec
}

func (c *skillCommand) Spec() commands.Spec {
	usage := "/" + c.skill.Name + " [args]"
	if c.skill.ArgumentHint != "" {
		usage = "/" + c.skill.Name + " " + c.skill.ArgumentHint
	}
	return commands.Spec{
		Name:        c.skill.Name,
		Usage:       usage,
		Description: c.skill.Description,
		Category:    "prompt",
		Kind:        commands.KindSkill,
		Source:      c.skill.Source,
	}
}

func (c *skillCommand) Run(inv commands.Invocation) tea.Cmd {
	result, err := skill.ProcessInvocation(context.Background(), c.app.SkillCatalog, skill.InvokeInput{
		Name:      c.skill.Name,
		Args:      inv.RawArgs,
		SessionID: c.app.Session.SessionID(),
		Source:    skill.SourceUser,
	})
	if err == skill.ErrNotFound {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			fmt.Sprintf("Skill not found: %s", c.skill.Name)))
	}
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render(
			fmt.Sprintf("Failed to invoke skill: %v", err)))
	}
	return c.app.executeSkillInvocation(result)
}
