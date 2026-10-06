// Package extension loads what extends codebot — skills, sub-agents, MCP
// servers and hooks — from where they come from: built in, the user's, the
// project's and the plugins'. Of two resources of one name the project's
// wins over the user's, and the user's over a built-in one; a plugin's are
// named after it, so they take no other's name; hooks replace none, they
// all run.
//
// What runs code or lets calls through unasked runs only as the user agreed
// to it, item by item: see Surface and Consents. Their own settings and
// files they wrote; a project, shared, may come from anyone, and a plugin
// from its author. What is instructions for the model alone always loads.
// Consent follows the thing agreed to, not who declares it: a plugin a
// project declares runs as the user agreed to the plugin.
package extension

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/voocel/codebot/internal/agent/skill"
	"github.com/voocel/codebot/internal/agent/subagent"
	"github.com/voocel/codebot/internal/extension/hooks"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/lib/printable"
	"github.com/voocel/codebot/internal/lib/regular"
)

// Scope is where a resource comes from.
type Scope string

const (
	Builtin Scope = "builtin"
	User    Scope = "user"
	Project Scope = "project"
	// Session is a plugin given on the command line, for this session alone.
	Session Scope = "session"
)

// Set is the extensions in effect.
type Set struct {
	// Skills are the skill of each name that wins, by name.
	Skills []skill.Spec
	// Agents are the user's, the project's and the plugins' sub-agent of
	// each name that wins; subagent.Definitions sets them over the built-in
	// ones.
	Agents []subagent.AgentDefinition
	// MCP are the MCP server of each name that wins, by name.
	MCP []MCPServer
	// Hooks are the user's hooks, the project's, then the plugins'.
	Hooks []Hook
	// Plugins are the plugins given on the command line and those the
	// settings declare, the project's before the user's, in effect or not.
	Plugins []Plugin

	// Trust is how the user stands on the project's surface.
	Trust Trust
	// Granted are the project's grants in effect: of its hooks, MCP
	// servers, plugins, allow rules and roots, those the user agreed to.
	// See config.ForProject.
	Granted config.Settings

	// Shadowed are the resources another of their name replaces.
	Shadowed []Shadow
	// Problems are what was left out: files that failed to load, and the
	// project's settings it may not set. They are told as a terminal is
	// to show them.
	Problems []error
}

// Trust is how the user stands on a project's surface.
type Trust struct {
	// Root is the project's, "" for none: the home directory is no project.
	Root string
	Standing
	// Denied says the user does not trust the project: none of its surface
	// is in effect, nor are they asked about it.
	Denied bool
	// ForRun says --trust trusts the project for this run, whatever the user
	// decided.
	ForRun bool
}

// MCPServer is an MCP server and where it is configured: the settings of
// Scope, or the plugin those declare.
type MCPServer struct {
	Name   string
	Scope  Scope
	Plugin string // the plugin bringing it, "" for one in settings
	config.MCPServer
}

// Hook is a hook and where it is configured: the settings of Scope, or the
// plugin those declare.
type Hook struct {
	Event  string
	Scope  Scope
	Plugin string // the plugin bringing it, "" for one in settings
	config.HookEntry
}

// Shadow is a resource another of its name replaces.
type Shadow struct {
	Kind string // "skill", "agent", "MCP server" or "plugin"
	Name string
	// Lost and Won say where the replaced resource and the one replacing it
	// come from: a file, or "builtin".
	Lost, Won string
}

// Options says what to load.
type Options struct {
	// Cwd is where the extensions load for; Layers are the settings there.
	Cwd    string
	Layers config.Layers
	// Consents are what the user agreed to.
	Consents Consents
	// PluginDirs are plugins to load for this session alone, what they run
	// agreed to: those the user gave on the command line.
	PluginDirs []string
	// TrustAll agrees for this session to what the project and the plugins
	// it declares run.
	TrustAll bool
}

// Load loads the extensions in effect.
func Load(o Options) *Set {
	s := &Set{}
	l := o.Layers
	_, grants, refused := config.ForProject(l.Project)
	if len(refused) > 0 {
		s.problem(fmt.Errorf("%s: only %s may set %s; ignored",
			config.ProjectSettingsPath(l.Root), config.UserSettingsPath(), strings.Join(refused, ", ")))
	}
	items := grantItems(grants)
	surface := projectSurface(items, s.loadSkills(o.Cwd, l.Root))
	c := o.Consents.Projects[l.Root]
	if o.TrustAll {
		c = Consent{Surface: surface}
	}
	s.Trust = Trust{Root: l.Root, Standing: c.Standing(surface), Denied: c.Denied, ForRun: o.TrustAll}
	for i, spec := range s.Skills {
		if spec.Source == string(Project) {
			s.Skills[i].Privileged = s.Trust.Agreed.HasAll(skillItems(spec.Name, spec))
		}
	}
	s.Granted = grant(items, s.Trust.Agreed)

	s.loadAgents(o.Cwd, l.Root)
	s.loadMCP(l.Root, l.User.MCPServers, s.Granted.MCPServers)
	s.addHooks(hooksOf(User, l.User.Hooks))
	s.addHooks(hooksOf(Project, s.Granted.Hooks))
	s.loadPlugins(o, grants.Plugins)
	slices.SortFunc(s.Agents, func(a, b subagent.AgentDefinition) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(s.Skills, func(a, b skill.Spec) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(s.MCP, func(a, b MCPServer) int { return strings.Compare(a.Name, b.Name) })
	return s
}

// problem reports errs, told as a terminal is to show them: they quote
// files.
func (s *Set) problem(errs ...error) {
	for _, err := range errs {
		s.Problems = append(s.Problems, fmt.Errorf("%s", printable.Escape(err.Error())))
	}
}

// MCPConfig returns the MCP servers by name.
func (s *Set) MCPConfig() map[string]config.MCPServer {
	out := make(map[string]config.MCPServer, len(s.MCP))
	for _, srv := range s.MCP {
		out[srv.Name] = srv.MCPServer
	}
	return out
}

// HooksConfig returns the hooks by event.
func (s *Set) HooksConfig() config.HooksConfig {
	out := config.HooksConfig{}
	for _, h := range s.Hooks {
		out[h.Event] = append(out[h.Event], h.HookEntry)
	}
	return out
}

// dir is a directory resources load from.
type dir struct {
	scope Scope
	path  string
}

// skillDirs returns the directories skills load from at cwd, by precedence:
// the project's .codebot/skills, its .agents/skills from cwd up to its root,
// then the user's .codebot/skills and .agents/skills.
func skillDirs(cwd, root string) []dir {
	var dirs []dir
	if root != "" {
		dirs = append(dirs, dir{Project, filepath.Join(root, config.ConfigDir, "skills")})
		for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
			dirs = append(dirs, dir{Project, filepath.Join(d, ".agents", "skills")})
			if d == root || d == filepath.Dir(d) {
				break
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			dir{User, filepath.Join(home, config.ConfigDir, "skills")},
			dir{User, filepath.Join(home, ".agents", "skills")})
	}
	return dirs
}

// loadSkills loads the skills that win, and returns the project's among
// them, frozen: what its surface shows is what runs, until reloaded. Their
// privileges wait on the user's consent. A project's skill file leading
// outside it is left out: it would read the user's files into the prompt.
func (s *Set) loadSkills(cwd, root string) (project []skill.Spec) {
	won := map[string]skill.Spec{}
	add := func(spec skill.Spec) {
		if w, ok := won[spec.Name]; ok {
			s.Shadowed = append(s.Shadowed, Shadow{"skill", spec.Name, skillOrigin(spec), skillOrigin(w)})
			return
		}
		won[spec.Name] = spec
	}
	for _, d := range skillDirs(cwd, root) {
		specs, errs := skill.LoadDir(d.path)
		s.problem(errs...)
		for _, spec := range specs {
			spec.Source, spec.Privileged = string(d.scope), d.scope == User
			if d.scope == Project {
				var err error
				if _, err = regular.Within(root, spec.FilePath); err == nil {
					spec, err = spec.Freeze()
				}
				if err != nil {
					s.problem(err)
					continue
				}
			}
			add(spec)
		}
	}
	for _, spec := range skill.Bundled(cwd) {
		spec.Source = string(Builtin)
		add(spec)
	}
	for _, name := range slices.Sorted(maps.Keys(won)) {
		spec := won[name]
		s.Skills = append(s.Skills, spec)
		if spec.Source == string(Project) {
			project = append(project, spec)
		}
	}
	return project
}

func skillOrigin(spec skill.Spec) string {
	if spec.FilePath == "" {
		return string(Builtin)
	}
	return spec.FilePath
}

// loadAgents loads the sub-agents of the project's .codebot/agents over the
// user's. A project's file leading outside it is left out.
func (s *Set) loadAgents(cwd, root string) {
	var dirs []dir
	if root != "" {
		dirs = append(dirs, dir{Project, filepath.Join(root, config.ConfigDir, "agents")})
	}
	dirs = append(dirs, dir{User, filepath.Join(config.UserConfigDir(), "agents")})

	won := map[string]subagent.AgentDefinition{}
	for _, d := range dirs {
		defs, errs := subagent.LoadDir(d.path)
		s.problem(errs...)
		for _, def := range defs {
			if d.scope == Project {
				if _, err := regular.Within(root, def.Origin); err != nil {
					s.problem(err)
					continue
				}
			}
			if w, ok := won[def.Name]; ok {
				s.Shadowed = append(s.Shadowed, Shadow{"agent", def.Name, def.Origin, w.Origin})
				continue
			}
			won[def.Name] = def
			s.Agents = append(s.Agents, def)
		}
	}
	for _, def := range subagent.Definitions(cwd, "", nil) {
		if w, ok := won[def.Name]; ok {
			s.Shadowed = append(s.Shadowed, Shadow{"agent", def.Name, def.Origin, w.Origin})
		}
	}
}

// loadMCP sets the project's MCP servers over the user's. The ${VAR} in
// the environment and headers of the user's are expanded from codebot's;
// the project's are run as they read, which the user agreed to, never
// carrying a secret of the user's where a project says. A server that is
// not one kind is left out.
func (s *Set) loadMCP(root string, user, project map[string]config.MCPServer) {
	add := func(name string, scope Scope, srv config.MCPServer) {
		if err := srv.Check(); err != nil {
			s.problem(fmt.Errorf("MCP server %q: %w", name, err))
			return
		}
		if scope == User {
			srv = expandEnv(srv)
		}
		s.MCP = append(s.MCP, MCPServer{Name: name, Scope: scope, MCPServer: srv})
	}
	for _, name := range slices.Sorted(maps.Keys(user)) {
		if _, ok := project[name]; ok {
			s.Shadowed = append(s.Shadowed, Shadow{"MCP server", name, config.UserSettingsPath(), config.ProjectSettingsPath(root)})
			continue
		}
		add(name, User, user[name])
	}
	for _, name := range slices.Sorted(maps.Keys(project)) {
		add(name, Project, project[name])
	}
}

var reEnvVar = regexp.MustCompile(`\$\{([^}]+)\}`)

// expandEnv expands ${VAR} from codebot's environment in the values of
// srv's environment and headers.
func expandEnv(srv config.MCPServer) config.MCPServer {
	expand := func(m map[string]string) map[string]string {
		if m == nil {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = reEnvVar.ReplaceAllStringFunc(v, func(ref string) string { return os.Getenv(ref[2 : len(ref)-1]) })
		}
		return out
	}
	srv.Env, srv.Headers = expand(srv.Env), expand(srv.Headers)
	return srv
}

// addHooks adds the hooks that run, and reports the others.
func (s *Set) addHooks(hs []Hook) {
	for _, h := range hs {
		if err := hooks.Check(h.Event, h.HookEntry); err != nil {
			s.problem(fmt.Errorf("%s hook %s: %w, left out", h.Scope, h.Detail(), err))
			continue
		}
		s.Hooks = append(s.Hooks, h)
	}
}

// hooksOf lists the hooks of cfg, by event.
func hooksOf(scope Scope, cfg config.HooksConfig) []Hook {
	var out []Hook
	for _, event := range slices.Sorted(maps.Keys(cfg)) {
		for _, e := range cfg[event] {
			out = append(out, Hook{Event: event, Scope: scope, HookEntry: e})
		}
	}
	return out
}
