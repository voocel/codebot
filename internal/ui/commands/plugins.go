package commands

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/plugin"
	"github.com/voocel/codebot/internal/ui/tui"
)

// MCPReloadResult is the subset of MCP runtime status that plugin mutations
// surface back to the user. The host fills it in after rebuilding the MCP
// pool following a plugin enable/disable/trust/install/remove.
type MCPReloadResult struct {
	Connected int
	Failed    int
	Tools     int
	Errors    []string
}

// PluginsCommand drives /plugins — a multi-subcommand entry that inspects
// and mutates the plugin catalog. The fields below are the host hooks the
// mutating paths need; construct with a struct literal at registration time.
type PluginsCommand struct {
	Catalog        *plugin.Catalog
	Session        *agent.Session
	Cwd            string
	ReloadState    func() error
	RefreshRuntime func() (MCPReloadResult, error)
}

func (p *PluginsCommand) Spec() Spec {
	return Spec{
		Name:        "plugins",
		Usage:       "/plugins [list|show|validate|create|install|remove|enable|disable|trust] ...",
		Description: "Inspect or manage plugins",
		Category:    "info",
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
	if p.Catalog == nil {
		return tui.SendCommandResult(tui.CommandStyle.Render("Plugins\n\nNo plugin catalog loaded."))
	}

	plugins := p.Catalog.Plugins()
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
		fmt.Fprintf(&sb, "  commands:%d\n", pl.CommandCount())
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
	loaded, ok := p.find(args[0])
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
	fmt.Fprintf(&sb, "commands: %d\n", loaded.CommandCount())
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
	if p.Catalog == nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin catalog not loaded."))
	}
	if msg := p.guardRunning(); msg != "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render(msg))
	}

	action := strings.ToLower(strings.TrimSpace(args[0]))
	id := strings.TrimSpace(args[1])
	enable := false
	switch action {
	case "enable":
		enable = true
	case "disable":
		enable = false
	case "trust":
		return p.trust(id, args[2:])
	default:
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins [enable|disable|trust] ..."))
	}

	loaded, ok := p.find(id)
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin: " + id))
	}
	if err := plugin.SetEnabled(p.Cwd, loaded, enable); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin update failed: " + err.Error()))
	}
	if err := p.ReloadState(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin reload failed: " + err.Error()))
	}
	mcpResult, err := p.RefreshRuntime()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Runtime reload failed: " + err.Error()))
	}

	status := "disabled"
	if enable {
		status = "enabled"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin %s %s.", loaded.Manifest.ID, status)
	if loaded.MCPCount() > 0 || len(mcpResult.Errors) > 0 {
		fmt.Fprintf(&sb, " MCP runtime reloaded: %d connected, %d failed, %d tools.",
			mcpResult.Connected, mcpResult.Failed, mcpResult.Tools)
	}
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(sb.String()))
}

func (p *PluginsCommand) trust(id string, args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins trust <plugin-id> <trusted|untrusted>"))
	}
	if msg := p.guardRunning(); msg != "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render(msg))
	}
	loaded, ok := p.find(id)
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin: " + id))
	}
	trust := strings.ToLower(strings.TrimSpace(args[0]))
	switch trust {
	case plugin.TrustTrusted, plugin.TrustUntrusted:
	default:
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins trust <plugin-id> <trusted|untrusted>"))
	}
	if err := plugin.SetTrust(p.Cwd, loaded, trust); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin trust update failed: " + err.Error()))
	}
	if err := p.ReloadState(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin reload failed: " + err.Error()))
	}
	mcpResult, err := p.RefreshRuntime()
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Runtime reload failed: " + err.Error()))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin %s trust set to %s.", loaded.Manifest.ID, trust)
	if trust == plugin.TrustUntrusted {
		sb.WriteString(" MCP contributions are disabled and skill privileged fields are stripped.")
	} else if loaded.MCPCount() > 0 || len(mcpResult.Errors) > 0 {
		fmt.Fprintf(&sb, " MCP runtime reloaded: %d connected, %d failed, %d tools.",
			mcpResult.Connected, mcpResult.Failed, mcpResult.Tools)
	}
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(sb.String()))
}

func (p *PluginsCommand) create(args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins create <plugin-id> [project|user|--project|--user]"))
	}
	if msg := p.guardRunning(); msg != "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render(msg))
	}

	scope := plugin.ScopeProject
	if len(args) > 1 {
		switch strings.ToLower(strings.TrimSpace(args[1])) {
		case "project", "--project":
			scope = plugin.ScopeProject
		case "user", "--user":
			scope = plugin.ScopeUser
		default:
			return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins create <plugin-id> [project|user|--project|--user]"))
		}
	}

	created, err := plugin.Scaffold(plugin.ScaffoldInput{
		Cwd:   p.Cwd,
		ID:    args[0],
		Scope: scope,
	})
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin scaffold failed: " + err.Error()))
	}
	if err := p.ReloadState(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin reload failed: " + err.Error()))
	}
	if _, err := p.RefreshRuntime(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Runtime reload failed: " + err.Error()))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin scaffold created: %s (%s).\n", created.ID, created.Scope)
	fmt.Fprintf(&sb, "root: %s\n", created.RootDir)
	fmt.Fprintf(&sb, "manifest: %s\n", created.ManifestPath)
	sb.WriteString("next: edit plugin.json, add files under skills/ or commands/, then run /reload.")
	return tui.SendCommandResult(tui.SystemMsgStyle.Render(sb.String()))
}

func (p *PluginsCommand) validate(args []string) tea.Cmd {
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins validate <plugin-id|path>"))
	}

	target := strings.TrimSpace(args[0])
	var report *plugin.ValidationReport
	var err error

	if _, statErr := os.Stat(target); statErr == nil {
		report, err = plugin.ValidatePath(target, "external")
	} else {
		loaded, ok := p.find(target)
		if !ok {
			return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin or path: " + target))
		}
		report, err = plugin.ValidateLoaded(loaded)
	}
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
	fmt.Fprintf(&sb, "commands:  %d\n", report.CommandCount)
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
	if len(args) < 1 {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins install <path> [project|user|--project|--user]"))
	}
	if msg := p.guardRunning(); msg != "" {
		return tui.SendCommandResult(tui.ErrorStyle.Render(msg))
	}

	scope := plugin.ScopeProject
	if len(args) > 1 {
		switch strings.ToLower(strings.TrimSpace(args[1])) {
		case "project", "--project":
			scope = plugin.ScopeProject
		case "user", "--user":
			scope = plugin.ScopeUser
		default:
			return tui.SendCommandResult(tui.ErrorStyle.Render("Usage: /plugins install <path> [project|user|--project|--user]"))
		}
	}

	installed, err := plugin.InstallLocal(plugin.InstallInput{
		Cwd:        p.Cwd,
		SourcePath: args[0],
		Scope:      scope,
	})
	if err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin install failed: " + err.Error()))
	}
	if err := p.ReloadState(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin reload failed: " + err.Error()))
	}
	if _, err := p.RefreshRuntime(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Runtime reload failed: " + err.Error()))
	}

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

	loaded, ok := p.find(args[0])
	if !ok {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Unknown plugin: " + strings.TrimSpace(args[0])))
	}
	if loaded.Scope == "builtin" {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Builtin plugins cannot be removed."))
	}
	if err := plugin.Remove(p.Cwd, loaded); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin remove failed: " + err.Error()))
	}
	if err := p.ReloadState(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Plugin reload failed: " + err.Error()))
	}
	if _, err := p.RefreshRuntime(); err != nil {
		return tui.SendCommandResult(tui.ErrorStyle.Render("Runtime reload failed: " + err.Error()))
	}

	return tui.SendCommandResult(tui.SystemMsgStyle.Render("Plugin " + loaded.Manifest.ID + " removed."))
}

func (p *PluginsCommand) find(id string) (plugin.Loaded, bool) {
	if p.Catalog == nil {
		return plugin.Loaded{}, false
	}
	id = strings.ToLower(strings.TrimSpace(id))
	for _, loaded := range p.Catalog.Plugins() {
		if strings.ToLower(loaded.Manifest.ID) == id {
			return loaded, true
		}
	}
	return plugin.Loaded{}, false
}

// guardRunning returns a non-empty error message when a session is currently
// running, blocking mutating plugin operations until the user aborts.
func (p *PluginsCommand) guardRunning() string {
	if p.Session != nil && p.Session.IsRunning() {
		return "agent is running; press Esc to abort first"
	}
	return ""
}
