// Package extension loads skills, sub-agents, MCP servers and hooks from
// built-ins, the user, the project and plugins. The project wins name clashes
// over the user, and the user over built-ins; plugin resources are
// namespaced, and all hooks run.
//
// Anything that runs code or skips permission prompts needs the user's
// consent item by item (see Surface and Consents), because a project or a
// plugin may come from anyone. Consent belongs to the item, not to whoever
// declares it. Instructions for the model always load.
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

type Scope string

const (
	Builtin Scope = "builtin"
	User    Scope = "user"
	Project Scope = "project"
	// Session marks a plugin given on the command line, for this session only.
	Session Scope = "session"
)

type Set struct {
	// Skills, Agents and MCP hold the winner of each name.
	Skills []skill.Spec
	// Agents excludes the built-in ones; subagent.Definitions layers these
	// over them.
	Agents []subagent.AgentDefinition
	MCP    []MCPServer
	// Hooks are ordered user, project, then plugins.
	Hooks []Hook
	// Plugins lists command-line plugins, then the project's, then the
	// user's, including those not in effect.
	Plugins []Plugin

	Trust Trust
	// Granted holds the project grants the user agreed to. See
	// config.ForProject.
	Granted config.Settings

	Shadowed []Shadow
	// Problems lists what was left out: files that failed to load and
	// project settings only the user may set. They are escaped for the
	// terminal.
	Problems []error
}

type Trust struct {
	// Root is "" outside a project; the home directory never counts as one.
	Root string
	Standing
	// Denied means the user distrusts the project: none of its surface takes
	// effect and they are not asked about it.
	Denied bool
	// ForRun is set by --trust, which overrides the saved decision for this
	// run.
	ForRun bool
}

func (t Trust) Ask() Surface {
	if t.Denied {
		return nil
	}
	return t.Standing.Ask()
}

type MCPServer struct {
	Name   string
	Scope  Scope
	Plugin string // "" when configured in settings
	config.MCPServer
}

type Hook struct {
	Event  string
	Scope  Scope
	Plugin string // "" when configured in settings
	config.HookEntry
}

// Shadow is a resource replaced by another of the same name.
type Shadow struct {
	Kind string // "skill", "agent", "MCP server" or "plugin"
	Name string
	// Lost and Won are the files of the replaced and the winning resource,
	// or "builtin".
	Lost, Won string
}

type Options struct {
	Cwd      string
	Layers   config.Layers
	Consents Consents
	// PluginDirs are command-line plugins, loaded for this session only with
	// implicit consent.
	PluginDirs []string
	// TrustAll consents, for this session, to everything the project and
	// its declared plugins run.
	TrustAll bool
}

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

// problem escapes errs for the terminal, since they may quote file contents.
func (s *Set) problem(errs ...error) {
	for _, err := range errs {
		s.Problems = append(s.Problems, fmt.Errorf("%s", printable.Escape(err.Error())))
	}
}

func (s *Set) MCPConfig() map[string]config.MCPServer {
	out := make(map[string]config.MCPServer, len(s.MCP))
	for _, srv := range s.MCP {
		out[srv.Name] = srv.MCPServer
	}
	return out
}

func (s *Set) HooksConfig() config.HooksConfig {
	out := config.HooksConfig{}
	for _, h := range s.Hooks {
		out[h.Event] = append(out[h.Event], h.HookEntry)
	}
	return out
}

type dir struct {
	scope Scope
	path  string
}

// skillDirs returns the skill directories, highest precedence first.
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

// loadSkills returns the project's winning skills. They are frozen, so what
// the user consents to is exactly what runs until the next reload. A project
// skill whose file resolves outside the project is skipped, since it would
// pull the user's files into the prompt.
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

// loadAgents skips project agent files that resolve outside the project.
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

// loadMCP expands ${VAR} only in the user's servers. Project servers run
// exactly as written and consented to, so a project cannot send the user's
// secrets to a server of its choosing.
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

func expandEnv(srv config.MCPServer) config.MCPServer {
	expand := func(v string) string {
		return reEnvVar.ReplaceAllStringFunc(v, func(ref string) string { return os.Getenv(ref[2 : len(ref)-1]) })
	}
	expandAll := func(m map[string]string) map[string]string {
		if m == nil {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = expand(v)
		}
		return out
	}
	srv.Env, srv.Headers = expandAll(srv.Env), expandAll(srv.Headers)
	if srv.OAuth != nil {
		oauth := *srv.OAuth
		oauth.ClientSecret = expand(oauth.ClientSecret)
		srv.OAuth = &oauth
	}
	return srv
}

func (s *Set) addHooks(hs []Hook) {
	for _, h := range hs {
		if err := hooks.Check(h.Event, h.HookEntry); err != nil {
			s.problem(fmt.Errorf("%s hook %s: %w, left out", h.Scope, h.Detail(), err))
			continue
		}
		s.Hooks = append(s.Hooks, h)
	}
}

func hooksOf(scope Scope, cfg config.HooksConfig) []Hook {
	var out []Hook
	for _, event := range slices.Sorted(maps.Keys(cfg)) {
		for _, e := range cfg[event] {
			out = append(out, Hook{Event: event, Scope: scope, HookEntry: e})
		}
	}
	return out
}
