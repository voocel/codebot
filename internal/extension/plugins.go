package extension

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/voocel/codebot/internal/extension/plugin"
	"github.com/voocel/codebot/internal/infra/config"
)

// Plugins live under ~/.codebot/plugins: the commits fetched, in cache/;
// what each keeps across updates, in data/; and the commit each git source
// is at, in lock.json.
func pluginsDir() string { return filepath.Join(config.UserConfigDir(), "plugins") }

// CacheDir is where the commits fetched of git plugins are kept.
func CacheDir() string { return filepath.Join(pluginsDir(), "cache") }

// DataDir is where plugins keep their data, each in its own directory.
func DataDir() string { return filepath.Join(pluginsDir(), "data") }

// MarketplacesDir is where the git marketplaces are mirrored.
func MarketplacesDir() string { return filepath.Join(pluginsDir(), "marketplaces") }

// SweepCache clears the cache of the commits no source is locked at, two
// weeks after it finds them so: a session may run one still.
func SweepCache() error {
	lock, err := ReadLock()
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for source, l := range lock {
		src, err := plugin.ParseSource(source, "")
		if err != nil {
			return fmt.Errorf("%s: %w", lockPath(), err)
		}
		keep[plugin.Cached(src, CacheDir(), l.Commit)] = true
	}
	return plugin.Sweep(CacheDir(), keep, time.Now())
}

// PluginState is where a plugin stands.
type PluginState string

const (
	PluginOn       PluginState = "on"
	PluginOff      PluginState = "off"           // the user turned it off in this project
	PluginShadowed PluginState = "shadowed"      // another of its name is in its stead
	PluginHeld     PluginState = "untrusted"     // the project declaring it is not trusted
	PluginMissing  PluginState = "not installed" // a git source not fetched, or no longer cached
	PluginBroken   PluginState = "broken"
)

// Plugin is a plugin the settings declare, and where it stands.
type Plugin struct {
	// Source is the plugin's source as declared, and Scope whose settings
	// declare it.
	Source string
	Scope  Scope
	// Dir is a local plugin's directory, "" for a git one.
	Dir string
	// Commit is the commit a git plugin is locked at, "" for a local one
	// and one never fetched.
	Commit string
	State  PluginState
	// Err says why the plugin is broken.
	Err error
	// Plugin is the plugin as read, nil where it is missing or broken.
	*plugin.Plugin
}

// Title names the plugin: by its name, or where it was not read, its
// source.
func (pl Plugin) Title() string {
	if pl.Plugin == nil {
		return pl.Source
	}
	return pl.Name
}

// Is reports whether ref names the plugin: its name, or its source as
// declared.
func (pl Plugin) Is(ref string) bool {
	return pl.Plugin != nil && pl.Name == ref || pl.Source == ref
}

// Locked is the commit a git source is at, and the plugin's surface there,
// which the user agreed to.
type Locked struct {
	Commit  string  `json:"commit"`
	Surface Surface `json:"surface,omitempty"`
}

func lockPath() string { return filepath.Join(pluginsDir(), "lock.json") }

// ReadLock returns the commit each git source is at, by source.
func ReadLock() (map[string]Locked, error) {
	all := map[string]Locked{}
	data, err := os.ReadFile(lockPath())
	if errors.Is(err, fs.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("%s: %w", lockPath(), err)
	}
	return all, nil
}

// Lock puts the git source at the commit l.
func Lock(source string, l Locked) error {
	if err := os.MkdirAll(pluginsDir(), 0o755); err != nil {
		return err
	}
	unlock, err := config.LockFile(lockPath())
	if err != nil {
		return err
	}
	defer unlock()
	all, err := ReadLock()
	if err != nil {
		return err
	}
	all[source] = l
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(lockPath(), data, 0o600)
}

// PluginBase is the directory a scope's plugin paths are relative to: that
// of the settings file declaring them.
func PluginBase(scope Scope, root string) string {
	if scope == Project {
		return filepath.Dir(config.ProjectSettingsPath(root))
	}
	return filepath.Dir(config.UserSettingsPath())
}

// readPlugins reads the plugins the layers declare, the project's first:
// those of local directories, and git ones at their locked commit.
func (s *Set) readPlugins(layers config.Layers, lock map[string]Locked) []Plugin {
	var out []Plugin
	for _, l := range []struct {
		scope   Scope
		sources []string
	}{{Project, layers.Project.Plugins}, {User, layers.User.Plugins}} {
		for _, raw := range l.sources {
			pl := Plugin{Source: raw, Scope: l.scope}
			problems, err := pl.read(PluginBase(l.scope, layers.Root), lock)
			switch {
			case err != nil:
				pl.State, pl.Err = PluginBroken, err
			case pl.Plugin == nil:
				pl.State = PluginMissing
			}
			s.Problems = append(s.Problems, problems...)
			out = append(out, pl)
		}
	}
	return out
}

// read reads the plugin, which it leaves nil when it is not installed.
func (pl *Plugin) read(base string, lock map[string]Locked) (problems []error, err error) {
	src, err := plugin.ParseSource(pl.Source, base)
	if err != nil {
		return nil, err
	}
	if pl.Dir = src.Dir; pl.Dir != "" {
		pl.Plugin, problems, err = plugin.Read(pl.Dir, DataDir())
		return problems, err
	}
	l, ok := lock[src.String()]
	if !ok {
		return nil, nil
	}
	pl.Commit = l.Commit
	if _, err := os.Stat(plugin.Cached(src, CacheDir(), l.Commit)); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	pl.Plugin, problems, err = plugin.ReadCached(src, CacheDir(), l.Commit, DataDir())
	return problems, err
}

// addPlugins adds the plugins on: those read but for an untrusted
// project's, the ones the user turned off, and a second of one name.
func (s *Set) addPlugins(plugins []Plugin, disabled []string) {
	won := map[string]Plugin{}
	for _, pl := range plugins {
		switch {
		case pl.Scope == Project && !s.Trusted:
			pl.State = PluginHeld
		case pl.Plugin == nil:
		case won[pl.Name].Plugin != nil:
			pl.State = PluginShadowed
			s.Shadowed = append(s.Shadowed, Shadow{"plugin", pl.Name, pl.Source, won[pl.Name].Source})
		case slices.Contains(disabled, "plugin:"+pl.Name):
			pl.State = PluginOff
		default:
			pl.State = PluginOn
			won[pl.Name] = pl
			s.contribute(pl)
		}
		s.Plugins = append(s.Plugins, pl)
	}
}

// contribute adds what a plugin brings, named after it: skill and agent
// "<plugin>:<name>", MCP server "<plugin>_<server>". A plugin's name holds
// no ":" or "_", so these never take another's name, but for a settings
// server's, which wins. Its hooks run beside the settings'. Its data
// directory is made for what it runs to keep data in.
func (s *Set) contribute(pl Plugin) {
	if err := os.MkdirAll(pl.Data, 0o700); err != nil {
		s.Problems = append(s.Problems, err)
	}
	for _, spec := range pl.Skills {
		spec.Name, spec.Source, spec.Privileged = pl.Name+":"+spec.Name, pl.Name, true
		s.Skills = append(s.Skills, spec)
	}
	for _, def := range pl.Agents {
		def.Name = pl.Name + ":" + def.Name
		s.Agents = append(s.Agents, def)
	}
	for _, name := range slices.Sorted(maps.Keys(pl.MCP)) {
		full := pl.Name + "_" + name
		if slices.ContainsFunc(s.MCP, func(m MCPServer) bool { return m.Name == full }) {
			s.Shadowed = append(s.Shadowed, Shadow{"MCP server", full, pl.Root, "settings"})
			continue
		}
		s.MCP = append(s.MCP, MCPServer{Name: full, Scope: pl.Scope, Plugin: pl.Name, MCPServer: pl.MCP[name]})
	}
	for _, h := range hooksOf(pl.Scope, pl.Hooks) {
		h.Plugin = pl.Name
		s.Hooks = append(s.Hooks, h)
	}
}

// PluginSurface is what a plugin runs or lets through: its MCP servers, its
// hooks, and what its skills may do only where trusted, named as it brings
// them. Its directory and its data's are told as ${PLUGIN_ROOT} and
// ${PLUGIN_DATA}: moving to another commit's directory runs nothing new.
// Its servers run in its directory unless they say otherwise, which goes
// untold.
func PluginSurface(p *plugin.Plugin) Surface {
	var s Surface
	relative := strings.NewReplacer(p.Root, "${PLUGIN_ROOT}", p.Data, "${PLUGIN_DATA}")
	for name, srv := range p.MCP {
		if srv.Cwd == p.Root {
			srv.Cwd = ""
		}
		s = append(s, NewItem("mcp", relative.Replace(MCPServer{Name: p.Name + "_" + name, Plugin: p.Name, MCPServer: srv}.Detail())))
	}
	for _, spec := range p.Skills {
		for _, priv := range spec.Privileges() {
			s = append(s, NewItem("skill", p.Name+":"+spec.Name+" "+priv))
		}
	}
	for _, h := range hooksOf("", p.Hooks) {
		s = append(s, NewItem("hook", p.Name+": "+h.Detail()))
	}
	return sorted(s)
}
