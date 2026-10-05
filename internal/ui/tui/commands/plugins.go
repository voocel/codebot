package commands

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/ui/tui"
)

// PluginsCommand drives /plugins — a multi-subcommand entry that inspects
// and mutates the plugin catalog.
type PluginsCommand struct {
	app   *app.App
	table *Table
}

func (p *PluginsCommand) Spec() Spec {
	return Spec{
		Name:        "plugins",
		Usage:       "/plugins [list|show|validate|create|install|remove|enable|disable|trust] ...",
		Description: "Inspect or manage plugins",
		Kind:        KindBuiltin,
	}
}

func (p *PluginsCommand) Run(inv Invocation) tea.Cmd {
	args := inv.Args
	if len(args) == 0 {
		return p.list()
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "list":
		return p.list()
	case "show":
		return p.show(args[1:])
	case "validate":
		return p.validate(args[1:])
	case "create":
		return p.create(args[1:])
	case "install":
		return p.install(args[1:])
	case "remove":
		return p.remove(args[1:])
	case "enable", "disable", "trust":
		return p.mutate(args)
	}
	return tui.SendCommandResult(tui.ErrorStyle.Render(
		"Usage: /plugins [list|show|validate|create|install|remove|enable|disable|trust] ..."))
}

func (p *PluginsCommand) list() tea.Cmd {
	plugins := p.app.Plugins()
	if len(plugins) == 0 {
		return tui.SendCommandResult(tui.CommandStyle.Render("Plugins\n\nNo plugins loaded."))
	}

	var sb strings.Builder
	sb.WriteString("Plugins\n\n")
	for _, pl := range plugins {
		status := "enabled"
		if !pl.State.Enabled {
			status = "disabled"
		}
		fmt.Fprintf(&sb, "%s (%s)\n", pl.Manifest.Name, pl.Manifest.ID)
		fmt.Fprintf(&sb, "  version: %s\n", pl.Manifest.Version)
		fmt.Fprintf(&sb, "  scope:   %s\n", pl.Scope)
		fmt.Fprintf(&sb, "  status:  %s\n", status)
		fmt.Fprintf(&sb, "  trust:   %s\n", pl.State.Trust)
		fmt.Fprintf(&sb, "  skills:  %d\n", pl.SkillCount())
		fmt.Fprintf(&sb, "  mcp:     %d\n", pl.MCPCount())
		if desc := strings.TrimSpace(pl.Manifest.Description); desc != "" {
			fmt.Fprintf(&sb, "  about:   %s\n", desc)
		}
		sb.WriteString("\n")
	}

	return tui.SendCommandResult(tui.CommandStyle.Render(strings.TrimRight(sb.String(), "\n")))
}

func (p *PluginsCommand) show(args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins show <plugin-id>"))
	}
	loaded, ok := p.app.Plugin(args[0])
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin: " + strings.TrimSpace(args[0])))
	}

	status := "enabled"
	if !loaded.State.Enabled {
		status = "disabled"
	}
	var sb strings.Builder
	sb.WriteString("Plugin\n\n")
	fmt.Fprintf(&sb, "name:     %s\n", loaded.Manifest.Name)
	fmt.Fprintf(&sb, "id:       %s\n", loaded.Manifest.ID)
	fmt.Fprintf(&sb, "version:  %s\n", loaded.Manifest.Version)
	fmt.Fprintf(&sb, "scope:    %s\n", loaded.Scope)
	fmt.Fprintf(&sb, "status:   %s\n", status)
	fmt.Fprintf(&sb, "trust:    %s\n", loaded.State.Trust)
	if loaded.RootDir != "" {
		fmt.Fprintf(&sb, "root:     %s\n", loaded.RootDir)
	}
	fmt.Fprintf(&sb, "skills:   %d\n", loaded.SkillCount())
	fmt.Fprintf(&sb, "mcp:      %d\n", loaded.MCPCount())
	if desc := strings.TrimSpace(loaded.Manifest.Description); desc != "" {
		fmt.Fprintf(&sb, "about:    %s\n", desc)
	}
	if !loaded.IsTrusted() {
		sb.WriteString("policy:   untrusted plugins cannot contribute MCP servers and their skills run without privileged fields\n")
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(sb.String()))
}

func (p *PluginsCommand) mutate(args []string) tea.Cmd {
	if len(args) < 2 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins [enable|disable|trust] ..."))
	}
	if msg := p.guardRunning(); msg != "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render(msg))
	}

	action := strings.ToLower(strings.TrimSpace(args[0]))
	id := strings.TrimSpace(args[1])
	if action == "trust" {
		return p.trust(id, args[2:])
	}
	enable := action == "enable"
	loaded, ok := p.app.Plugin(id)
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin: " + id))
	}
	report, err := p.app.SetPluginEnabled(context.Background(), id, enable)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin update failed: " + err.Error()))
	}
	p.table.Rebuild()

	status := "disabled"
	if enable {
		status = "enabled"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin %s %s.", loaded.Manifest.ID, status)
	if loaded.MCPCount() > 0 || len(report.MCP.Errors) > 0 {
		sb.WriteString(" " + formatMCPReload(report.MCP))
	}
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(sb.String()))
}

func (p *PluginsCommand) trust(id string, args []string) tea.Cmd {
	usage := "Usage: /plugins trust <plugin-id> <trusted|untrusted>"
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render(usage))
	}
	var trusted bool
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "trusted":
		trusted = true
	case "untrusted":
	default:
		return tui.SendCommandResult(tui.ErrorStyle.Render(usage))
	}
	loaded, ok := p.app.Plugin(id)
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin: " + id))
	}
	report, err := p.app.SetPluginTrusted(context.Background(), id, trusted)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin trust update failed: " + err.Error()))
	}
	p.table.Rebuild()

	var sb strings.Builder
	if trusted {
		fmt.Fprintf(&sb, "Plugin %s trust set to trusted.", loaded.Manifest.ID)
		if loaded.MCPCount() > 0 || len(report.MCP.Errors) > 0 {
			sb.WriteString(" " + formatMCPReload(report.MCP))
		}
	} else {
		fmt.Fprintf(&sb, "Plugin %s trust set to untrusted. MCP contributions are disabled and skill privileged fields are stripped.", loaded.Manifest.ID)
	}
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(sb.String()))
}

func (p *PluginsCommand) create(args []string) tea.Cmd {
	usage := "Usage: /plugins create <plugin-id> [project|user|--project|--user]"
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render(usage))
	}
	if msg := p.guardRunning(); msg != "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render(msg))
	}
	user, ok := parseScope(args[1:])
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render(usage))
	}

	created, _, err := p.app.CreatePlugin(context.Background(), args[0], user)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin scaffold failed: " + err.Error()))
	}
	p.table.Rebuild()

	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin scaffold created: %s (%s).\n", created.ID, created.Scope)
	fmt.Fprintf(&sb, "root: %s\n", created.RootDir)
	fmt.Fprintf(&sb, "manifest: %s\n", created.ManifestPath)
	sb.WriteString("next: edit plugin.json, add skills under skills/, then run /reload.")
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(sb.String()))
}

func (p *PluginsCommand) validate(args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins validate <plugin-id|path>"))
	}
	report, err := p.app.ValidatePlugin(strings.TrimSpace(args[0]))
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin validation failed: " + err.Error()))
	}

	var sb strings.Builder
	sb.WriteString("Plugin Validation\n\n")
	fmt.Fprintf(&sb, "id:        %s\n", report.Manifest.ID)
	fmt.Fprintf(&sb, "name:      %s\n", report.Manifest.Name)
	fmt.Fprintf(&sb, "version:   %s\n", report.Manifest.Version)
	fmt.Fprintf(&sb, "scope:     %s\n", report.Scope)
	fmt.Fprintf(&sb, "root:      %s\n", report.RootDir)
	if report.State != nil {
		status := "enabled"
		if !report.State.Enabled {
			status = "disabled"
		}
		fmt.Fprintf(&sb, "status:    %s\n", status)
		fmt.Fprintf(&sb, "trust:     %s\n", report.State.Trust)
	}
	fmt.Fprintf(&sb, "skills:    %d\n", report.SkillCount)
	fmt.Fprintf(&sb, "mcp:       %d\n", report.MCPCount)
	fmt.Fprintf(&sb, "summary:   %s\n", report.Summary())
	if len(report.Warnings) > 0 {
		sb.WriteString("\nWarnings:\n")
		for _, warning := range report.Warnings {
			sb.WriteString("  - " + warning + "\n")
		}
	}
	if len(report.Errors) > 0 {
		sb.WriteString("\nErrors:\n")
		for _, issue := range report.Errors {
			sb.WriteString("  - " + issue + "\n")
		}
	}
	return tui.SendCommandResult(tui.CommandStyle.Render(strings.TrimRight(sb.String(), "\n")))
}

func (p *PluginsCommand) install(args []string) tea.Cmd {
	usage := "Usage: /plugins install <path> [project|user|--project|--user]"
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render(usage))
	}
	if msg := p.guardRunning(); msg != "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render(msg))
	}
	user, ok := parseScope(args[1:])
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render(usage))
	}

	installed, _, err := p.app.InstallPlugin(context.Background(), args[0], user)
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin install failed: " + err.Error()))
	}
	p.table.Rebuild()

	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin installed: %s (%s).\n", installed.ID, installed.Scope)
	fmt.Fprintf(&sb, "root: %s\n", installed.RootDir)
	fmt.Fprintf(&sb, "manifest: %s", installed.ManifestPath)
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(sb.String()))
}

func (p *PluginsCommand) remove(args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins remove <plugin-id>"))
	}
	if msg := p.guardRunning(); msg != "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render(msg))
	}
	id := strings.TrimSpace(args[0])
	if _, err := p.app.RemovePlugin(context.Background(), id); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin remove failed: " + err.Error()))
	}
	p.table.Rebuild()
	return tui.SendCommandResult(tui.SystemMsgStyle.Render("Plugin " + id + " removed."))
}

// guardRunning returns a non-empty error message while the agent runs,
// blocking mutating plugin operations until the user aborts.
func (p *PluginsCommand) guardRunning() string {
	if p.app.Current().Status().Running {
		return "agent is running; press Esc to abort first"
	}
	return ""
}

// parseScope reads an optional project/user scope argument; user reports
// the user scope.
func parseScope(args []string) (user, ok bool) {
	if len(args) == 0 {
		return false, true
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "project", "--project":
		return false, true
	case "user", "--user":
		return true, true
	}
	return false, false
}

func formatMCPReload(r app.MCPReport) string {
	return fmt.Sprintf("MCP runtime reloaded: %d connected, %d failed, %d tools.", r.Connected, len(r.Errors), r.Tools)
}
