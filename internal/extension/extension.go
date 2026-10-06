// Package extension loads what extends codebot — skills, sub-agents, MCP
// servers and hooks — from where they come from: built in, the user's, the
// project's and the plugins'. Of two resources of one name the project's
// wins over the user's, and the user's over a built-in one; a plugin's are
// named after it, so they take no other's name; hooks replace none, they
// all run.
//
// A project is shared and may come from anyone. What in it runs code or lets
// calls through unasked, its surface, takes effect only once the user trusts
// it; the rest, instructions for the model, always does.
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
)

// Scope is where a resource comes from.
type Scope string

const (
	Builtin Scope = "builtin"
	User    Scope = "user"
	Project Scope = "project"
)

// Set is the extensions in effect.
type Set struct {
	// Skills are the skill of each name that wins, by name.
	Skills []skill.Spec
	// Agents are the user's and the project's sub-agent of each name that
	// wins; subagent.Definitions sets them over the built-in ones.
	Agents []subagent.AgentDefinition
	// MCP are the MCP server of each name that wins, by name.
	MCP []MCPServer
	// Hooks are the user's hooks, then the project's.
	Hooks []Hook
	// Plugins are the plugins the settings declare, the project's first,
	// on or not.
	Plugins []Plugin

	// Surface is the project's surface, in effect or not.
	Surface Surface
	// Trusted says the project's surface is in effect.
	Trusted bool

	// Shadowed are the resources another of their name replaces.
	Shadowed []Shadow
	// Problems are what was left out: files that failed to load, and the
	// project's settings it may not set.
	Problems []error
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
	// Trust decides from the project's surface whether it takes effect.
	Trust func(Surface) bool
	// Disabled are the plugins the user turned off in the project,
	// "plugin:<name>"; see Workspace.
	Disabled []string
}

// Load loads the extensions in effect.
func Load(o Options) *Set {
	s := &Set{}
	layers := o.Layers
	projectSkills := s.loadSkills(o.Cwd, layers.Root)
	lock, err := ReadLock()
	if err != nil {
		s.Problems = append(s.Problems, err)
	}
	plugins := s.readPlugins(layers, lock)
	s.Surface = surface(layers.Project, projectSkills, plugins)
	s.Trusted = o.Trust(s.Surface)
	for i := range s.Skills {
		if s.Skills[i].Source == string(Project) {
			s.Skills[i].Privileged = s.Trusted
		}
	}
	s.loadAgents(o.Cwd, layers.Root)

	project, refused := config.ForProject(layers.Project, s.Trusted)
	if len(refused) > 0 {
		s.Problems = append(s.Problems, fmt.Errorf("%s: only %s may set %s; ignored",
			config.ProjectSettingsPath(layers.Root), config.UserSettingsPath(), strings.Join(refused, ", ")))
	}
	s.loadMCP(layers.Root, layers.User.MCPServers, project.MCPServers)
	s.addHooks(hooksOf(User, layers.User.Hooks))
	s.addHooks(hooksOf(Project, project.Hooks))
	s.addPlugins(plugins, o.Disabled)
	slices.SortFunc(s.Agents, func(a, b subagent.AgentDefinition) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(s.Skills, func(a, b skill.Spec) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(s.MCP, func(a, b MCPServer) int { return strings.Compare(a.Name, b.Name) })
	return s
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
// them. Their privileges wait on the project's trust.
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
		s.Problems = append(s.Problems, errs...)
		for _, spec := range specs {
			spec.Source, spec.Privileged = string(d.scope), d.scope == User
			if d.scope == Project {
				// What its surface shows is what runs, until reloaded.
				var err error
				if spec, err = spec.Freeze(); err != nil {
					s.Problems = append(s.Problems, err)
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
// user's.
func (s *Set) loadAgents(cwd, root string) {
	var dirs []dir
	if root != "" {
		dirs = append(dirs, dir{Project, filepath.Join(root, config.ConfigDir, "agents")})
	}
	dirs = append(dirs, dir{User, filepath.Join(config.UserConfigDir(), "agents")})

	won := map[string]subagent.AgentDefinition{}
	for _, d := range dirs {
		defs, errs := subagent.LoadDir(d.path)
		s.Problems = append(s.Problems, errs...)
		for _, def := range defs {
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

// loadMCP sets the project's MCP servers over the user's, the ${VAR} in
// their environment and headers expanded from codebot's. A server that is
// not one kind is left out.
func (s *Set) loadMCP(root string, user, project map[string]config.MCPServer) {
	add := func(name string, scope Scope, srv config.MCPServer) {
		if err := srv.Check(); err != nil {
			s.Problems = append(s.Problems, fmt.Errorf("MCP server %q: %w", name, err))
			return
		}
		s.MCP = append(s.MCP, MCPServer{Name: name, Scope: scope, MCPServer: expandEnv(srv)})
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
			s.Problems = append(s.Problems, fmt.Errorf("%s hook %s: %w, left out", h.Scope, h.Detail(), err))
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
