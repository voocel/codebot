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

// ~/.codebot/plugins holds fetched commits in cache/ and, in data/, each
// source's data that survives updates.
func pluginsDir() string { return filepath.Join(config.UserConfigDir(), "plugins") }

func cacheDir() string { return filepath.Join(pluginsDir(), "cache") }

func dataDir() string { return filepath.Join(pluginsDir(), "data") }

type PluginState string

const (
	PluginOn           PluginState = "on"
	PluginShadowed     PluginState = "shadowed"      // another plugin of the same name won
	PluginUntrusted    PluginState = "untrusted"     // the user has not agreed to the project declaring it
	PluginNotInstalled PluginState = "not installed" // a git plugin the user has not agreed to yet
	PluginNotCached    PluginState = "not cached"    // the agreed commit is no longer cached
	PluginWaiting      PluginState = "waiting"       // it runs something the user has not agreed to, so none of it runs
	PluginBroken       PluginState = "broken"
)

type Plugin struct {
	// Source is as written in settings or on the command line.
	Source string
	Scope  Scope
	// Src is Source parsed relative to the declaring settings file; zero if
	// it does not parse.
	Src plugin.Source
	// Commit is the agreed commit of a git plugin; "" for a local plugin or
	// one not agreed to yet.
	Commit string
	State  PluginState
	// Err is set when State is PluginBroken.
	Err error
	// Surface is everything the plugin runs; New is the part the user has
	// not agreed to, which keeps all of it off.
	Surface, New Surface
	// Plugin is nil unless State is PluginOn, PluginShadowed or
	// PluginWaiting.
	*plugin.Plugin
}

func (pl Plugin) Title() string {
	if pl.Plugin == nil {
		return pl.Source
	}
	return pl.Name
}

func (pl Plugin) Is(ref string) bool {
	return pl.Plugin != nil && pl.Name == ref || pl.Source == ref
}

func PluginBase(scope Scope, root string) string {
	if scope == Project {
		return filepath.Dir(config.ProjectSettingsPath(root))
	}
	return filepath.Dir(config.UserSettingsPath())
}

// loadPlugins gives command-line plugins precedence over the project's, and
// the project's over the user's.
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
		// A plugin waiting for consent keeps its name, though it runs
		// nothing: precedence goes by where it is declared.
		if pl.State == PluginOn || pl.State == PluginWaiting {
			if w, ok := won[pl.Name]; ok {
				pl.State = PluginShadowed
				s.Shadowed = append(s.Shadowed, Shadow{"plugin", pl.Name, pl.Source, w.Source})
			} else {
				won[pl.Name] = pl
				if pl.State == PluginOn {
					s.contribute(pl)
				}
			}
		}
		s.Plugins = append(s.Plugins, pl)
	}
}

// readPlugin reads a git plugin only at the agreed commit and only from the
// cache; loading never fetches. Command-line plugins, and the project's under
// --trust, need no consent.
func (s *Set) readPlugin(pl *Plugin, o Options) {
	src := pl.Src
	c, ok := o.Consents.Plugins[src.ID()]
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
	pl.Surface, pl.State = PluginSurface(pl.Plugin), PluginOn
	if implicit := pl.Scope == Session || pl.Scope == Project && o.TrustAll; !implicit {
		pl.New = pl.Surface.Missing(c.Surface)
	}
	if len(pl.New) > 0 {
		pl.State = PluginWaiting
	}
}

// contribute namespaces the plugin's resources: skills and agents as
// "<plugin>:<name>", MCP servers as "<plugin>_<server>". Plugin names contain
// no ":" or "_", so only a server from settings can clash, and it wins.
func (s *Set) contribute(pl Plugin) {
	if err := os.MkdirAll(pl.Data, 0o700); err != nil {
		s.problem(err)
	}
	for _, spec := range pl.Skills {
		spec.Name, spec.Source, spec.Privileged = pl.Name+":"+spec.Name, "plugin", true
		s.Skills = append(s.Skills, spec)
	}
	for _, def := range pl.Agents {
		def.Name = pl.Name + ":" + def.Name
		s.Agents = append(s.Agents, def)
	}
	for _, name := range slices.Sorted(maps.Keys(pl.MCP)) {
		srv := MCPServer{Name: pl.Name + "_" + name, Scope: pl.Scope, Plugin: pl.Name, MCPServer: pl.MCP[name]}
		if i := slices.IndexFunc(s.MCP, func(m MCPServer) bool { return m.Name == srv.Name }); i >= 0 {
			s.Shadowed = append(s.Shadowed, Shadow{"MCP server", srv.Name, pl.Root, settingsPath(s.MCP[i].Scope, s.Trust.Root)})
			continue
		}
		s.MCP = append(s.MCP, srv)
	}
	for _, h := range hooksOf(pl.Scope, pl.Hooks) {
		h.Plugin = pl.Name
		s.Hooks = append(s.Hooks, h)
	}
}

func settingsPath(scope Scope, root string) string {
	if scope == Project {
		return config.ProjectSettingsPath(root)
	}
	return config.UserSettingsPath()
}

// PluginSurface lists the plugin's MCP servers, hooks and skill privileges.
// Paths are written as ${PLUGIN_ROOT} and ${PLUGIN_DATA}, so moving to another
// commit's directory alone does not need new consent.
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

// pluginServer returns server name as the surface shows it: paths become
// ${PLUGIN_ROOT} and ${PLUGIN_DATA}, and a cwd equal to the default, the
// plugin root, is dropped.
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

// ReadPlugin reads a plugin for the user to review. A git plugin is fetched
// at commit, or at its ref when commit is "", and the resolved commit is
// returned.
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

func LatestCommit(ctx context.Context, src plugin.Source) (string, error) {
	return plugin.Latest(ctx, src, cacheDir())
}

// AgreeToPlugin records the user's agreement to everything the plugin runs,
// surface, at commit for a git plugin.
func AgreeToPlugin(src plugin.Source, commit string, surface Surface) error {
	return EditConsents(func(c *Consents) {
		c.Plugins[src.ID()] = PluginConsent{Commit: commit, Surface: surface}
	})
}

// SweepCache removes cached commits unread for two weeks.
func SweepCache() error { return plugin.SweepCache(cacheDir(), time.Now()) }
