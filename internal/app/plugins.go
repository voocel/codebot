package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/voocel/codebot/internal/plugin"
)

// Plugin is a loaded plugin.
type Plugin = plugin.Loaded

// Plugins lists the loaded plugins, enabled or not.
func (a *App) Plugins() []Plugin {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.plugins.Plugins()
}

// Plugin finds a loaded plugin by ID, ignoring case.
func (a *App) Plugin(id string) (Plugin, bool) {
	id = strings.TrimSpace(id)
	for _, p := range a.Plugins() {
		if strings.EqualFold(p.Manifest.ID, id) {
			return p, true
		}
	}
	return Plugin{}, false
}

// The changes below write the plugins on disk, then reload them and what they
// contribute.

// SetPluginEnabled enables or disables a plugin.
func (a *App) SetPluginEnabled(ctx context.Context, id string, enabled bool) (ReloadReport, error) {
	return a.changePlugin(ctx, id, func(p plugin.Loaded) error { return plugin.SetEnabled(a.cwd, p, enabled) })
}

// SetPluginTrusted trusts a plugin or not. An untrusted plugin contributes no
// MCP servers, and its skills lose their privileged fields.
func (a *App) SetPluginTrusted(ctx context.Context, id string, trusted bool) (ReloadReport, error) {
	trust := plugin.TrustUntrusted
	if trusted {
		trust = plugin.TrustTrusted
	}
	return a.changePlugin(ctx, id, func(p plugin.Loaded) error { return plugin.SetTrust(a.cwd, p, trust) })
}

// RemovePlugin deletes a project or user plugin.
func (a *App) RemovePlugin(ctx context.Context, id string) (ReloadReport, error) {
	return a.changePlugin(ctx, id, func(p plugin.Loaded) error { return plugin.Remove(a.cwd, p) })
}

// CreatePlugin scaffolds an empty plugin for the project, or for the user.
func (a *App) CreatePlugin(ctx context.Context, id string, user bool) (*plugin.ScaffoldResult, ReloadReport, error) {
	created, err := plugin.Scaffold(plugin.ScaffoldInput{Cwd: a.cwd, ID: id, Scope: pluginScope(user)})
	if err != nil {
		return nil, ReloadReport{}, err
	}
	report, err := a.ReloadPlugins(ctx)
	return created, report, err
}

// InstallPlugin copies the plugin at path in, for the project or the user.
func (a *App) InstallPlugin(ctx context.Context, path string, user bool) (*plugin.InstallResult, ReloadReport, error) {
	installed, err := plugin.InstallLocal(plugin.InstallInput{Cwd: a.cwd, SourcePath: path, Scope: pluginScope(user)})
	if err != nil {
		return nil, ReloadReport{}, err
	}
	report, err := a.ReloadPlugins(ctx)
	return installed, report, err
}

// ValidatePlugin checks the plugin at a path, or the loaded plugin with that
// ID.
func (a *App) ValidatePlugin(target string) (*plugin.ValidationReport, error) {
	if _, err := os.Stat(target); err == nil {
		return plugin.ValidatePath(target, "external")
	}
	p, ok := a.Plugin(target)
	if !ok {
		return nil, fmt.Errorf("unknown plugin or path: %s", target)
	}
	return plugin.ValidateLoaded(p)
}

func (a *App) changePlugin(ctx context.Context, id string, change func(plugin.Loaded) error) (ReloadReport, error) {
	p, ok := a.Plugin(id)
	if !ok {
		return ReloadReport{}, fmt.Errorf("unknown plugin: %s", id)
	}
	if err := change(p); err != nil {
		return ReloadReport{}, err
	}
	return a.ReloadPlugins(ctx)
}

func pluginScope(user bool) string {
	if user {
		return plugin.ScopeUser
	}
	return plugin.ScopeProject
}
