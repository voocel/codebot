package commands

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/voocel/codebot/internal/app"
)

const pluginsUsage = "/plugins [list | show <id> | validate <id|path> | create <id> [user] | install <path> [user] | remove <id> | enable <id> | disable <id> | trust <id> trusted|untrusted]"

func plugins(a *app.App) Command {
	return Command{Name: "plugins", Args: "[action]", Description: "Inspect and manage plugins: list, show, install, enable…", Run: func(line string) tea.Cmd {
		args := ParseArgs(line)
		if len(args) == 0 {
			return pluginList(a)
		}
		verb, rest := strings.ToLower(args[0]), args[1:]
		if verb == "list" {
			return pluginList(a)
		}
		if verb == "show" || verb == "validate" {
			if len(rest) == 0 {
				return fail("Usage: " + pluginsUsage)
			}
			if verb == "show" {
				return pluginShow(a, rest[0])
			}
			return pluginValidate(a, rest[0])
		}
		// The rest change what the agent runs with.
		if a.Current().Status().Running {
			return fail("/plugins " + verb + " waits for the agent to finish; press esc to stop it.")
		}
		if len(rest) == 0 {
			return fail("Usage: " + pluginsUsage)
		}
		ctx := context.Background()
		switch verb {
		case "enable", "disable":
			p, ok := a.Plugin(rest[0])
			if !ok {
				return fail("Unknown plugin " + rest[0])
			}
			r, err := a.SetPluginEnabled(ctx, rest[0], verb == "enable")
			if err != nil {
				return fail("Could not " + verb + " the plugin: " + err.Error())
			}
			return note(fmt.Sprintf("Plugin %s %sd. %s", p.Manifest.ID, verb, mcpReloaded(p, r.MCP)))
		case "trust":
			if len(rest) < 2 || (rest[1] != "trusted" && rest[1] != "untrusted") {
				return fail("Usage: /plugins trust <id> trusted|untrusted")
			}
			p, ok := a.Plugin(rest[0])
			if !ok {
				return fail("Unknown plugin " + rest[0])
			}
			r, err := a.SetPluginTrusted(ctx, rest[0], rest[1] == "trusted")
			if err != nil {
				return fail("Could not change the plugin's trust: " + err.Error())
			}
			if rest[1] == "untrusted" {
				return note("Plugin " + p.Manifest.ID + " is untrusted: its MCP servers are off and its skills lose their privileged fields.")
			}
			return note("Plugin " + p.Manifest.ID + " is trusted. " + mcpReloaded(p, r.MCP))
		case "create", "install":
			user, ok := scope(rest[1:])
			if !ok {
				return fail("Usage: " + pluginsUsage)
			}
			if verb == "create" {
				c, _, err := a.CreatePlugin(ctx, rest[0], user)
				if err != nil {
					return fail("Could not create the plugin: " + err.Error())
				}
				return output(fmt.Sprintf("Created plugin %s (%s) in %s.\nEdit %s, add skills under skills/, then /reload.", c.ID, c.Scope, c.RootDir, c.ManifestPath))
			}
			c, _, err := a.InstallPlugin(ctx, rest[0], user)
			if err != nil {
				return fail("Could not install the plugin: " + err.Error())
			}
			return output(fmt.Sprintf("Installed plugin %s (%s) in %s.", c.ID, c.Scope, c.RootDir))
		case "remove":
			if _, err := a.RemovePlugin(ctx, rest[0]); err != nil {
				return fail("Could not remove the plugin: " + err.Error())
			}
			return note("Plugin " + rest[0] + " removed.")
		}
		return fail("Usage: " + pluginsUsage)
	}}
}

func pluginList(a *app.App) tea.Cmd {
	ps := a.Plugins()
	if len(ps) == 0 {
		return note("No plugins loaded.")
	}
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteString("\n")
		}
		state := "enabled"
		if !p.State.Enabled {
			state = "disabled"
		}
		fmt.Fprintf(&b, "%s  %s · %s · %s · %d skills · %d MCP", p.Manifest.ID, p.Manifest.Version, p.Scope, state, p.SkillCount(), p.MCPCount())
		if d := strings.TrimSpace(p.Manifest.Description); d != "" {
			b.WriteString("\n  " + d)
		}
	}
	return output(b.String())
}

func pluginShow(a *app.App, id string) tea.Cmd {
	p, ok := a.Plugin(id)
	if !ok {
		return fail("Unknown plugin " + id)
	}
	state := "enabled"
	if !p.State.Enabled {
		state = "disabled"
	}
	lines := []string{
		p.Manifest.Name + " (" + p.Manifest.ID + ") " + p.Manifest.Version,
		fmt.Sprintf("%s · %s · trust %s · %d skills · %d MCP", p.Scope, state, p.State.Trust, p.SkillCount(), p.MCPCount()),
	}
	if p.RootDir != "" {
		lines = append(lines, p.RootDir)
	}
	if d := strings.TrimSpace(p.Manifest.Description); d != "" {
		lines = append(lines, d)
	}
	if !p.IsTrusted() {
		lines = append(lines, "Untrusted: it cannot add MCP servers, and its skills run without privileged fields.")
	}
	return output(strings.Join(lines, "\n"))
}

func pluginValidate(a *app.App, target string) tea.Cmd {
	r, err := a.ValidatePlugin(target)
	if err != nil {
		return fail("Validation failed: " + err.Error())
	}
	lines := []string{
		fmt.Sprintf("%s (%s) %s · %s", r.Manifest.Name, r.Manifest.ID, r.Manifest.Version, r.Scope),
		fmt.Sprintf("%d skills · %d MCP · %s", r.SkillCount, r.MCPCount, r.Summary()),
	}
	for _, w := range r.Warnings {
		lines = append(lines, "warning: "+w)
	}
	for _, e := range r.Errors {
		lines = append(lines, "error: "+e)
	}
	return output(strings.Join(lines, "\n"))
}

func mcpReloaded(p app.Plugin, r app.MCPReport) string {
	if p.MCPCount() == 0 && len(r.Errors) == 0 {
		return ""
	}
	return fmt.Sprintf("MCP reloaded: %d connected, %d failed, %d tools.", r.Connected, len(r.Errors), r.Tools)
}

// scope reads an optional "user" or "project" argument: user reports the
// former.
func scope(args []string) (user, ok bool) {
	if len(args) == 0 {
		return false, true
	}
	switch strings.TrimPrefix(strings.ToLower(args[0]), "--") {
	case "project":
		return false, true
	case "user":
		return true, true
	}
	return false, false
}

// ParseArgs splits s into words, keeping quoted runs together.
func ParseArgs(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			flush()
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			flush()
			quote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}
