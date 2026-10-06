package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/voocel/codebot/internal/extension"
	"github.com/voocel/codebot/internal/extension/plugin"
	"github.com/voocel/codebot/internal/infra/config"
)

// Plugin is a plugin the settings declare, and where it stands.
type Plugin = extension.Plugin

// Plugin states; see extension.PluginState.
const (
	PluginOn       = extension.PluginOn
	PluginOff      = extension.PluginOff
	PluginShadowed = extension.PluginShadowed
	PluginHeld     = extension.PluginHeld
	PluginMissing  = extension.PluginMissing
	PluginBroken   = extension.PluginBroken
)

// Plugins lists the plugins the settings declare, the project's first.
func (a *App) Plugins() []Plugin { return a.Extensions().Plugins }

// plugin finds the declared plugin ref names, by its name or its source as
// declared. A ref naming more than one names none.
func (a *App) plugin(ref string) (Plugin, error) {
	var found []Plugin
	for _, pl := range a.Plugins() {
		if pl.Is(ref) {
			found = append(found, pl)
		}
	}
	switch len(found) {
	case 0:
		return Plugin{}, fmt.Errorf("no plugin %q: /plugins lists them", ref)
	case 1:
		return found[0], nil
	}
	var where []string
	for _, pl := range found {
		where = append(where, pl.Source+" in "+a.settingsPath(pl.Scope))
	}
	return Plugin{}, fmt.Errorf("%q names more than one plugin, %s: name it by its source, or edit the settings", ref, strings.Join(where, " and "))
}

// settingsPath is the settings file of scope.
func (a *App) settingsPath(scope extension.Scope) string {
	if scope == extension.Project {
		return config.ProjectSettingsPath(a.root)
	}
	return config.UserSettingsPath()
}

// PluginOffer is a plugin fetched and read, for the user to agree to what
// it runs before it is added.
type PluginOffer struct {
	// Source is the plugin's source, as the settings are to declare it.
	Source string
	// Project adds it to the project's settings, rather than the user's.
	Project bool
	// Commit is the commit fetched of a git plugin, "" for a local one.
	Commit string
	*plugin.Plugin
	// Surface is what it runs or lets through.
	Surface Surface
	// Problems are what of it failed to load, left out.
	Problems []error

	src plugin.Source
	// declared says the settings declare it already, not installed or
	// broken.
	declared bool
}

// OfferPlugin fetches the plugin at source and reads it, for AddPlugin to
// add once the user agrees. A git source is fetched at its ref into the
// cache; a local one, a path from the working directory, is read where it
// is. A source the settings declare that is not installed or is broken is
// offered anew.
func (a *App) OfferPlugin(ctx context.Context, source string, project bool) (*PluginOffer, error) {
	scope := extension.User
	if project {
		if a.root == "" {
			return nil, errors.New("the home directory is no project: add the plugin for yourself")
		}
		scope = extension.Project
	}
	source = strings.TrimSpace(source)
	if isListing(source) {
		var err error
		if source, err = a.listed(ctx, source); err != nil {
			return nil, err
		}
	}
	src, err := plugin.ParseSource(source, a.cwd)
	if err != nil {
		return nil, err
	}
	base := extension.PluginBase(scope, a.root)
	o := &PluginOffer{Source: a.declare(source, src, scope), Project: project, src: src}
	for _, pl := range a.Plugins() {
		if declared, err := plugin.ParseSource(pl.Source, base); pl.Scope != scope || err != nil || declared != src {
			continue
		}
		if pl.Plugin != nil {
			return nil, fmt.Errorf("%s is declared already", pl.Source)
		}
		o.Source, o.declared = pl.Source, true
	}
	if src.Dir != "" {
		o.Plugin, o.Problems, err = plugin.Read(src.Dir, extension.DataDir())
	} else if o.Commit, err = plugin.Fetch(ctx, src, extension.CacheDir(), ""); err == nil {
		o.Plugin, o.Problems, err = plugin.ReadCached(src, extension.CacheDir(), o.Commit, extension.DataDir())
	}
	if err != nil {
		return nil, err
	}
	for _, pl := range a.Plugins() {
		if pl.Plugin != nil && pl.Name == o.Name {
			return nil, fmt.Errorf("a plugin named %s is declared already, from %s", o.Name, pl.Source)
		}
	}
	o.Surface = extension.PluginSurface(o.Plugin)
	return o, nil
}

// declare is how the settings of scope are to declare source, src parsed
// from it: a directory in the project relative to the project's settings,
// so that they hold wherever it is checked out; another given relative to
// the working directory as its absolute path; the rest as given.
func (a *App) declare(source string, src plugin.Source, scope extension.Scope) string {
	switch {
	case src.Dir == "":
		return source
	case scope == extension.Project && (src.Dir == a.root || strings.HasPrefix(src.Dir, a.root+string(filepath.Separator))):
		rel, err := filepath.Rel(extension.PluginBase(scope, a.root), src.Dir)
		if err != nil {
			return src.Dir
		}
		if rel = filepath.ToSlash(rel); rel != ".." && !strings.HasPrefix(rel, "../") {
			rel = "./" + rel
		}
		return rel
	case strings.HasPrefix(source, "."):
		return src.Dir
	}
	return source
}

// AddPlugin adds the plugin the user agreed to: it locks a git plugin at the
// commit offered, declares it in the settings unless they do already, and
// turns it on in this project, then reloads. A plugin added to a trusted
// project is trusted with it.
func (a *App) AddPlugin(ctx context.Context, o *PluginOffer) (ReloadReport, error) {
	if o.Commit != "" {
		if err := extension.Lock(o.src.String(), extension.Locked{Commit: o.Commit, Surface: o.Surface}); err != nil {
			return ReloadReport{}, err
		}
	}
	scope := extension.User
	if o.Project {
		scope = extension.Project
		if err := a.agree(append(Surface{extension.NewItem("plugin", o.Source)}, o.Surface...)); err != nil {
			return ReloadReport{}, err
		}
	}
	if !o.declared {
		if err := config.EditSettings(a.settingsPath(scope), func(s *config.Settings) { s.Plugins = append(s.Plugins, o.Source) }); err != nil {
			return ReloadReport{}, err
		}
	}
	if err := a.enable(o.Name, true); err != nil {
		return ReloadReport{}, err
	}
	return a.Reload(ctx)
}

// agree adds items to the user's trust in the project, when they trust it:
// they agreed to them as they asked for them.
func (a *App) agree(items Surface) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if d := a.decision; d != nil {
		if d.Trusted {
			a.decision = &extension.Decision{Trusted: true, Surface: d.Surface.With(items)}
		}
		return nil
	}
	return extension.EditWorkspace(a.decisions(), func(w *extension.Workspace) {
		if w.Trust != nil && w.Trust.Trusted {
			w.Trust.Surface = w.Trust.Surface.With(items)
		}
	})
}

// RemovePlugin removes the plugin ref names, by name or source, from the
// settings declaring it, then reloads.
func (a *App) RemovePlugin(ctx context.Context, ref string) (ReloadReport, error) {
	pl, err := a.plugin(ref)
	if err != nil {
		return ReloadReport{}, err
	}
	err = config.EditSettings(a.settingsPath(pl.Scope), func(s *config.Settings) {
		s.Plugins = slices.DeleteFunc(s.Plugins, func(src string) bool { return src == pl.Source })
	})
	if err != nil {
		return ReloadReport{}, err
	}
	return a.Reload(ctx)
}

// SetPluginEnabled turns the plugin named name on or off in this project,
// for the user alone, then reloads.
func (a *App) SetPluginEnabled(ctx context.Context, name string, on bool) (ReloadReport, error) {
	if !slices.ContainsFunc(a.Plugins(), func(pl Plugin) bool { return pl.Plugin != nil && pl.Name == name }) {
		return ReloadReport{}, fmt.Errorf("no plugin %q to turn on or off", name)
	}
	if err := a.enable(name, on); err != nil {
		return ReloadReport{}, err
	}
	return a.Reload(ctx)
}

// enable turns the plugin named name on or off in this project.
func (a *App) enable(name string, on bool) error {
	key := "plugin:" + name
	return extension.EditWorkspace(a.decisions(), func(w *extension.Workspace) {
		w.Disabled = slices.DeleteFunc(w.Disabled, func(d string) bool { return d == key })
		if !on {
			w.Disabled = append(w.Disabled, key)
		}
	})
}

// PluginUpdate is a git plugin fetched anew.
type PluginUpdate struct {
	Plugin Plugin
	// Commit is the commit fetched, the plugin's own when it is up to date.
	Commit string
	// Added is what the commit fetched runs that the plugin did not. The
	// update waits for the user to agree to it, in ApplyUpdate.
	Added Surface
	// Applied says the update applied: it ran nothing new.
	Applied bool
	Err     error

	source  string
	surface Surface
}

// UpdatePlugins fetches anew at their refs the git plugins in effect, or
// the one ref names: on, off, broken, or no longer cached; an untrusted
// project's, and one another is in the stead of, are left alone. An update
// that runs nothing new applies at once; the others wait for the user to
// agree to what they add, in ApplyUpdate.
func (a *App) UpdatePlugins(ctx context.Context, ref string) ([]PluginUpdate, error) {
	lock, err := extension.ReadLock()
	if err != nil {
		return nil, err
	}
	var out []PluginUpdate
	applied := false
	for _, pl := range a.Plugins() {
		if pl.Commit == "" || pl.State == PluginHeld || pl.State == PluginShadowed || ref != "" && !pl.Is(ref) {
			continue
		}
		u := a.update(ctx, pl, lock)
		applied = applied || u.Applied
		out = append(out, u)
	}
	if len(out) == 0 {
		if ref == "" {
			return nil, errors.New("no git plugins to update")
		}
		return nil, fmt.Errorf("no git plugin %q to update", ref)
	}
	if applied {
		if _, err := a.Reload(ctx); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (a *App) update(ctx context.Context, pl Plugin, lock map[string]extension.Locked) PluginUpdate {
	u := PluginUpdate{Plugin: pl}
	src, err := plugin.ParseSource(pl.Source, extension.PluginBase(pl.Scope, a.root))
	if err == nil {
		u.Commit, err = plugin.Fetch(ctx, src, extension.CacheDir(), "")
	}
	switch {
	case err != nil:
		u.Err = err
		return u
	case u.Commit == pl.Commit:
		// Fetched again, a commit no longer cached takes effect.
		u.Applied = pl.State == PluginMissing
		return u
	}
	p, _, err := plugin.ReadCached(src, extension.CacheDir(), u.Commit, extension.DataDir())
	if err != nil {
		u.Err = err
		return u
	}
	u.source, u.surface = src.String(), extension.PluginSurface(p)
	if u.Added = u.surface.Missing(lock[u.source].Surface); len(u.Added) == 0 {
		u.Err = extension.Lock(u.source, extension.Locked{Commit: u.Commit, Surface: u.surface})
		u.Applied = u.Err == nil
	}
	return u
}

// ApplyUpdate moves the plugin to the commit fetched, the user having
// agreed to what it adds, a project's plugin with the project; then
// reloads.
func (a *App) ApplyUpdate(ctx context.Context, u PluginUpdate) (ReloadReport, error) {
	if err := extension.Lock(u.source, extension.Locked{Commit: u.Commit, Surface: u.surface}); err != nil {
		return ReloadReport{}, err
	}
	if u.Plugin.Scope == extension.Project {
		if err := a.agree(u.Added); err != nil {
			return ReloadReport{}, err
		}
	}
	return a.Reload(ctx)
}

// fetchPlugins fetches the plugins missing that need no asking, then
// reloads if it fetched any. Those locked are fetched at the commit the
// user agreed to; a trusted project's new ones at their refs, the user
// having agreed to them as they trusted it, and what they run joins its
// surface, to be asked about. Then it sweeps the cache of the commits
// left behind. One fetch runs at a time.
func (a *App) fetchPlugins(ctx context.Context) (fetched, errs []string) {
	a.fetching.Lock()
	defer a.fetching.Unlock()
	for _, pl := range a.Plugins() {
		if pl.State != PluginMissing || pl.Commit == "" && pl.Scope != extension.Project {
			continue
		}
		if err := a.fetchPlugin(ctx, pl); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		fetched = append(fetched, pl.Source)
	}
	if len(fetched) > 0 {
		if err := a.reload(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := extension.SweepCache(); err != nil {
		errs = append(errs, "sweep the plugin cache: "+err.Error())
	}
	return fetched, errs
}

func (a *App) fetchPlugin(ctx context.Context, pl Plugin) error {
	src, err := plugin.ParseSource(pl.Source, extension.PluginBase(pl.Scope, a.root))
	if err != nil {
		return err
	}
	commit, err := plugin.Fetch(ctx, src, extension.CacheDir(), pl.Commit)
	if err != nil || pl.Commit != "" {
		return err
	}
	p, _, err := plugin.ReadCached(src, extension.CacheDir(), commit, extension.DataDir())
	if err != nil {
		return fmt.Errorf("%s: %w", pl.Source, err)
	}
	return extension.Lock(src.String(), extension.Locked{Commit: commit, Surface: extension.PluginSurface(p)})
}
