package extension

import (
	"context"
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

// Plugins live under ~/.codebot/plugins: the commits fetched, in cache/,
// and what each source's keeps across updates, in data/.
func pluginsDir() string { return filepath.Join(config.UserConfigDir(), "plugins") }

func cacheDir() string { return filepath.Join(pluginsDir(), "cache") }

func dataDir() string { return filepath.Join(pluginsDir(), "data") }

// PluginState is where a plugin stands.
type PluginState string

const (
	PluginOn           PluginState = "on"
	PluginShadowed     PluginState = "shadowed"      // another of its name is in its stead
	PluginUntrusted    PluginState = "untrusted"     // the project declaring it is not trusted to
	PluginNotInstalled PluginState = "not installed" // a git one the user has yet to agree to
	PluginNotCached    PluginState = "not cached"    // a git one agreed to at a commit no longer cached
	PluginBroken       PluginState = "broken"
)

// Plugin is a plugin given on the command line or declared in settings,
// and where it stands.
type Plugin struct {
	// Source is the plugin's source as given, and Scope whose settings
	// declare it, or Session.
	Source string
	Scope  Scope
	// Src is Source parsed, from the directory of the settings declaring
	// it; zero where it does not parse.
	Src plugin.Source
	// Commit is the commit of a git plugin the user agreed to, "" for a
	// local one and one they have yet to agree to.
	Commit string
	State  PluginState
	// Err says why the plugin is broken.
	Err error
	// Surface is what the plugin runs, and Agreed what of it the user agreed
	// to: of the rest, nothing runs.
	Surface, Agreed Surface
	// Plugin is the plugin as read, nil where it is held, missing or
	// broken.
	*plugin.Plugin
}

// Held returns what of the plugin's surface waits for the user to agree to
// it.
func (pl Plugin) Held() Surface { return pl.Surface.Missing(pl.Agreed) }

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

// PluginBase is the directory a scope's plugin paths are relative to: that
// of the settings file declaring them.
func PluginBase(scope Scope, root string) string {
	if scope == Project {
		return filepath.Dir(config.ProjectSettingsPath(root))
	}
	return filepath.Dir(config.UserSettingsPath())
}

// loadPlugins loads the plugins given on the command line, then those the
// project declares, then the user's: of two of one name, the first. Of the
// project's, those the user has not trusted it to declare are untrusted.
func (s *Set) loadPlugins(o Options, declared []string) {
	type decl struct {
		scope     Scope
		raw, base string
	}
	var decls []decl
	for _, dir := range o.PluginDirs {
		decls = append(decls, decl{Session, dir, o.Cwd})
	}
	for _, raw := range declared {
		decls = append(decls, decl{Project, raw, PluginBase(Project, o.Layers.Root)})
	}
	for _, raw := range o.Layers.User.Plugins {
		decls = append(decls, decl{User, raw, PluginBase(User, "")})
	}
	won := map[string]Plugin{}
	for _, d := range decls {
		pl := Plugin{Source: d.raw, Scope: d.scope}
		var err error
		switch pl.Src, err = plugin.ParseSource(d.raw, d.base); {
		case err != nil:
			pl.State, pl.Err = PluginBroken, err
		case d.scope == Project && !slices.Contains(s.Granted.Plugins, d.raw):
			pl.State = PluginUntrusted
		default:
			s.readPlugin(&pl, o)
		}
		if pl.State == PluginOn {
			if w, ok := won[pl.Name]; ok {
				pl.State = PluginShadowed
				s.Shadowed = append(s.Shadowed, Shadow{"plugin", pl.Name, pl.Source, w.Source})
			} else {
				won[pl.Name] = pl
				s.contribute(pl)
			}
		}
		s.Plugins = append(s.Plugins, pl)
	}
}

// readPlugin reads the plugin pl declares, and what of it the user agreed
// to run. A local one is read where it is; a git one at the commit the user
// agreed to, if it is cached. One given on the command line, or one a
// project trusted for the run declares, runs all it does.
func (s *Set) readPlugin(pl *Plugin, o Options) {
	src := pl.Src
	c, ok := o.Consents.Plugins[src.String()]
	data := plugin.DataDir(src, dataDir())
	var problems []error
	var err error
	switch {
	case src.Dir != "":
		pl.Plugin, problems, err = plugin.Read(src.Dir, data)
	case !ok:
		pl.State = PluginNotInstalled
		return
	default:
		pl.Commit = c.Commit
		if _, err := os.Stat(plugin.Cached(src, cacheDir(), c.Commit)); errors.Is(err, fs.ErrNotExist) {
			pl.State = PluginNotCached
			return
		}
		pl.Plugin, problems, err = plugin.ReadCached(src, cacheDir(), c.Commit, data)
	}
	s.problem(problems...)
	if err != nil {
		pl.State, pl.Plugin, pl.Err = PluginBroken, nil, err
		return
	}
	surface := PluginSurface(pl.Plugin)
	if pl.Scope == Session || pl.Scope == Project && o.TrustAll {
		c = Consent{Surface: surface}
	}
	pl.State, pl.Surface, pl.Agreed = PluginOn, surface, c.agreed(surface)
}

// contribute adds what a plugin brings, named after it: skill and agent
// "<plugin>:<name>", MCP server "<plugin>_<server>". A plugin's name holds
// no ":" or "_", so these never take another's name, but for a settings
// server's, which wins. Its hooks run beside the settings'. Of what runs,
// it brings what the user agreed to alone. Its data directory is made for
// what it runs to keep data in.
func (s *Set) contribute(pl Plugin) {
	if err := os.MkdirAll(pl.Data, 0o700); err != nil {
		s.problem(err)
	}
	for _, spec := range pl.Skills {
		name := pl.Name + ":" + spec.Name
		spec.Name, spec.Source, spec.Privileged = name, "plugin", pl.Agreed.HasAll(skillItems(name, spec))
		s.Skills = append(s.Skills, spec)
	}
	for _, def := range pl.Agents {
		def.Name = pl.Name + ":" + def.Name
		s.Agents = append(s.Agents, def)
	}
	for _, name := range slices.Sorted(maps.Keys(pl.MCP)) {
		if !pl.Agreed.Has(pluginServer(pl.Plugin, name).item()) {
			continue
		}
		srv := MCPServer{Name: pl.Name + "_" + name, Scope: pl.Scope, Plugin: pl.Name, MCPServer: pl.MCP[name]}
		if i := slices.IndexFunc(s.MCP, func(m MCPServer) bool { return m.Name == srv.Name }); i >= 0 {
			s.Shadowed = append(s.Shadowed, Shadow{"MCP server", srv.Name, pl.Root, settingsPath(s.MCP[i].Scope, s.Trust.Root)})
			continue
		}
		s.MCP = append(s.MCP, srv)
	}
	for _, h := range hooksOf(pl.Scope, pl.Hooks) {
		if h.Plugin = pl.Name; pl.Agreed.Has(h.item()) {
			s.Hooks = append(s.Hooks, h)
		}
	}
}

func settingsPath(scope Scope, root string) string {
	if scope == Project {
		return config.ProjectSettingsPath(root)
	}
	return config.UserSettingsPath()
}

// PluginSurface is what a plugin runs or lets through: its MCP servers, its
// hooks, and what its skills may do only where agreed to, named as it
// brings them. Its directory and its data's are told as ${PLUGIN_ROOT} and
// ${PLUGIN_DATA}: moving to another commit's directory runs nothing new.
func PluginSurface(p *plugin.Plugin) Surface {
	var s Surface
	for name := range p.MCP {
		s = append(s, pluginServer(p, name).item())
	}
	for _, spec := range p.Skills {
		s = append(s, skillItems(p.Name+":"+spec.Name, spec)...)
	}
	for _, h := range hooksOf("", p.Hooks) {
		h.Plugin = p.Name
		s = append(s, h.item())
	}
	return sorted(s)
}

// pluginServer is the server name of p as its surface tells it: its paths
// in the plugin and its data as ${PLUGIN_ROOT} and ${PLUGIN_DATA}, and the
// plugin's directory, where it runs unless it says otherwise, untold.
func pluginServer(p *plugin.Plugin, name string) MCPServer {
	srv := p.MCP[name]
	relative := strings.NewReplacer(p.Root, "${PLUGIN_ROOT}", p.Data, "${PLUGIN_DATA}").Replace
	if srv.Cwd == p.Root {
		srv.Cwd = ""
	}
	srv.Command, srv.Cwd = relative(srv.Command), relative(srv.Cwd)
	srv.Args = slices.Clone(srv.Args)
	for i, a := range srv.Args {
		srv.Args[i] = relative(a)
	}
	srv.Env = maps.Clone(srv.Env)
	for k, v := range srv.Env {
		srv.Env[k] = relative(v)
	}
	return MCPServer{Name: p.Name + "_" + name, Plugin: p.Name, MCPServer: srv}
}

// ReadPlugin reads the plugin at src for the user to agree to what it runs:
// a local one where it is; a git one at commit or, where that is "", at its
// ref, fetched into the cache unless it is there. It returns the commit of
// a git one.
func ReadPlugin(ctx context.Context, src plugin.Source, commit string) (p *plugin.Plugin, got string, problems []error, err error) {
	data := plugin.DataDir(src, dataDir())
	if src.Dir != "" {
		p, problems, err = plugin.Read(src.Dir, data)
		return p, "", problems, err
	}
	if got, err = plugin.Fetch(ctx, src, cacheDir(), commit); err != nil {
		return nil, "", nil, err
	}
	p, problems, err = plugin.ReadCached(src, cacheDir(), got, data)
	if err != nil {
		return nil, "", nil, fmt.Errorf("%s: %w", src, err)
	}
	return p, got, problems, nil
}

// AgreeToPlugin records that the user agreed to the plugin at src running
// surface, at commit for a git one.
func AgreeToPlugin(src plugin.Source, commit string, surface Surface) error {
	return EditConsents(func(c *Consents) { c.Plugins[src.String()] = Consent{Commit: commit, Surface: surface} })
}

// SweepPlugins clears what of the plugins the user agreed to of none: the
// commits cached, and the data of the sources, two weeks after it finds
// them so, as a session may use one still. The data of the plugins given
// on the command line, in pluginDirs, stays too.
func SweepPlugins(pluginDirs []string) error {
	c, err := ReadConsents()
	if err != nil {
		return err
	}
	commits, data := map[string]bool{}, map[string]bool{}
	keep := func(source string, commit string) error {
		src, err := plugin.ParseSource(source, "")
		if err != nil {
			return err
		}
		data[plugin.DataDir(src, dataDir())] = true
		if commit != "" {
			commits[plugin.Cached(src, cacheDir(), commit)] = true
		}
		return nil
	}
	for _, dir := range pluginDirs {
		if err := keep(dir, ""); err != nil {
			return err
		}
	}
	for source, consent := range c.Plugins {
		if err := keep(source, consent.Commit); err != nil {
			return fmt.Errorf("%s: %w", consentsPath(), err)
		}
	}
	now := time.Now()
	return errors.Join(plugin.SweepCache(cacheDir(), commits, now), plugin.SweepData(dataDir(), data, now))
}
