package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/voocel/codebot/internal/extension"
	"github.com/voocel/codebot/internal/extension/plugin"
	"github.com/voocel/codebot/internal/infra/config"
)

// Plugin is a plugin given on the command line or declared in settings,
// and where it stands.
type Plugin = extension.Plugin

// PluginContent is a plugin as read: its manifest and what it brings.
type PluginContent = plugin.Plugin

// Plugin states; see extension.PluginState.
const (
	PluginOn           = extension.PluginOn
	PluginShadowed     = extension.PluginShadowed
	PluginUntrusted    = extension.PluginUntrusted
	PluginNotInstalled = extension.PluginNotInstalled
	PluginNotCached    = extension.PluginNotCached
	PluginBroken       = extension.PluginBroken
)

// Plugins lists the plugins given on the command line, then those the
// settings declare, the project's first.
func (a *App) Plugins() []Plugin { return a.Extensions().Plugins }

// PluginOffer is a plugin read for the user to agree to what it runs: one
// to add, one the settings declare that runs what they have yet to agree
// to, or a git one's newer commit.
type PluginOffer struct {
	// Source is the plugin's source as the settings declare it, or are to,
	// and Scope whose settings.
	Source string
	Scope  extension.Scope
	// Commit is the commit read of a git plugin.
	Commit string
	*plugin.Plugin
	// Surface is all the plugin runs, and New what of it the user has yet
	// to decide on.
	Surface, New Surface
	// Problems are what of it failed to load, left out.
	Problems []error

	src plugin.Source
	// declare says the settings are to declare it.
	declare bool
}

// offer reads the plugin at src for the user to agree to: see
// extension.ReadPlugin.
func (a *App) offer(ctx context.Context, src plugin.Source, commit string) (*PluginOffer, error) {
	consents, err := extension.ReadConsents()
	if err != nil {
		return nil, err
	}
	p, got, problems, err := extension.ReadPlugin(ctx, src, commit)
	if err != nil {
		return nil, err
	}
	surface := extension.PluginSurface(p)
	return &PluginOffer{Commit: got, Plugin: p, Surface: surface, New: consents.Plugins[src.String()].Standing(surface).Ask(), Problems: problems, src: src}, nil
}

// OfferPlugin reads the plugin at source, a git repository fetched at its
// ref or a directory from the working directory, for AcceptPlugin to add
// to the user's settings or, with project, the project's, once the user
// agrees to what it runs.
func (a *App) OfferPlugin(ctx context.Context, source string, project bool) (*PluginOffer, error) {
	scope := extension.User
	if project {
		if a.Trust().Root == "" {
			return nil, errors.New("the home directory is no project: add the plugin for yourself")
		}
		scope = extension.Project
	}
	source = strings.TrimSpace(source)
	src, err := plugin.ParseSource(source, a.cwd)
	if err != nil {
		return nil, err
	}
	for _, pl := range a.Plugins() {
		if pl.Scope == scope && pl.Src == src {
			return nil, fmt.Errorf("%s is declared already; /plugins install readies it", pl.Source)
		}
	}
	o, err := a.offer(ctx, src, "")
	if err != nil {
		return nil, err
	}
	for _, pl := range a.Plugins() {
		if pl.Plugin != nil && pl.Name == o.Name {
			return nil, fmt.Errorf("a plugin named %s is declared already, from %s", o.Name, pl.Source)
		}
	}
	o.Source, o.Scope, o.declare = a.declare(source, src, scope), scope, true
	return o, nil
}

// declare is how the settings of scope are to declare source, src parsed
// from it: a directory in the project relative to the project's settings,
// so that they hold wherever it is checked out; another given relative to
// the working directory as its absolute path; the rest as given.
func (a *App) declare(source string, src plugin.Source, scope extension.Scope) string {
	root := a.Trust().Root
	switch {
	case src.Dir == "":
		return source
	case scope == extension.Project && (src.Dir == root || strings.HasPrefix(src.Dir, root+string(filepath.Separator))):
		rel, _ := filepath.Rel(extension.PluginBase(scope, root), src.Dir)
		if rel = filepath.ToSlash(rel); rel != ".." && !strings.HasPrefix(rel, "../") {
			rel = "./" + rel
		}
		return rel
	case strings.HasPrefix(source, "."):
		return src.Dir
	}
	return source
}

// InstallPlugins readies the plugins the settings declare that wait for
// the user. A git one they agreed to is fetched at its commit where it is
// not cached; the others are offered for them to decide on what they run:
// a git one they have yet to agree to, fetched at its ref, and one that
// runs what they have yet to decide on. It reloads when it fetched any.
func (a *App) InstallPlugins(ctx context.Context) (offers []*PluginOffer, fetched []string, errs []error) {
	for _, pl := range a.Plugins() {
		var o *PluginOffer
		var err error
		switch {
		case pl.State == PluginNotCached:
			if _, _, _, err = extension.ReadPlugin(ctx, pl.Src, pl.Commit); err == nil {
				fetched = append(fetched, pl.Source)
			}
		case pl.State == PluginNotInstalled:
			o, err = a.offer(ctx, pl.Src, "")
		case pl.State == PluginOn && len(pl.Ask()) > 0:
			o, err = a.offer(ctx, pl.Src, pl.Commit)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", pl.Source, err))
		}
		if o != nil {
			o.Source, o.Scope = pl.Source, pl.Scope
			offers = append(offers, o)
		}
	}
	if len(fetched) > 0 {
		if _, err := a.refresh(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return offers, fetched, errs
}

// PluginUpdate is a git plugin fetched anew at its ref.
type PluginUpdate struct {
	Plugin Plugin
	// Commit is the commit fetched, the plugin's own when it is up to date.
	Commit string
	// Offer is the commit fetched where it runs what the user has yet to
	// agree to: the plugin stays at its own until they do, in
	// AcceptPlugin. Nil, a commit fetched anew applied.
	Offer *PluginOffer
	Err   error
}

// UpdatePlugins fetches anew at their refs the git plugins the user agreed
// to, or the one ref names. An update that runs nothing new applies at
// once; the others wait for the user to agree to what they add.
func (a *App) UpdatePlugins(ctx context.Context, ref string) ([]PluginUpdate, error) {
	var out []PluginUpdate
	applied := false
	for _, pl := range a.Plugins() {
		if pl.Commit == "" || pl.State == PluginShadowed || ref != "" && !pl.Is(ref) {
			continue
		}
		u := PluginUpdate{Plugin: pl}
		o, err := a.offer(ctx, pl.Src, "")
		switch {
		case err != nil:
			u.Err = err
		case o.Commit == pl.Commit:
			u.Commit = o.Commit
			// Fetched again, a commit no longer cached takes effect.
			applied = applied || pl.State == PluginNotCached
		case len(o.New) > 0:
			o.Source, o.Scope = pl.Source, pl.Scope
			u.Commit, u.Offer = o.Commit, o
		default:
			u.Commit, u.Err = o.Commit, extension.DecidePlugin(pl.Src, o.Commit, o.Surface, nil, nil)
			applied = applied || u.Err == nil
		}
		out = append(out, u)
	}
	if len(out) == 0 {
		if ref == "" {
			return nil, errors.New("no git plugins to update")
		}
		return nil, fmt.Errorf("no git plugin %q to update", ref)
	}
	if applied {
		if _, err := a.refresh(ctx); err != nil {
			return out, err
		}
	}
	return out, nil
}

// PluginUpdates names the plugins in effect from git whose ref has moved
// off the commit agreed to: what /plugins update would update. It asks the
// remotes for their commits alone, fetching nothing; one that does not
// answer is left out, untold.
func (a *App) PluginUpdates(ctx context.Context) []string {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		names []string
	)
	for _, pl := range a.Plugins() {
		if pl.State != PluginOn || pl.Commit == "" {
			continue
		}
		wg.Go(func() {
			if latest, err := extension.LatestCommit(ctx, pl.Src); err == nil && latest != "" && latest != pl.Commit {
				mu.Lock()
				names = append(names, pl.Name)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	slices.Sort(names)
	return names
}

// AcceptPlugin records what the user decided of the plugin offered, at its
// commit: of what it runs new, they agreed to agreed and declined the rest.
// It declares the plugin in the settings where it is to be, then puts it in
// effect. A plugin the user adds to the project the project declares as
// they trust it: they wrote it there.
func (a *App) AcceptPlugin(ctx context.Context, o *PluginOffer, agreed Surface) (ReloadReport, error) {
	if err := extension.DecidePlugin(o.src, o.Commit, o.Surface, o.New, agreed); err != nil {
		return ReloadReport{}, err
	}
	if o.declare {
		if err := a.editSettings(o.Scope, func(s *config.Settings) { s.Plugins = append(s.Plugins, o.Source) }); err != nil {
			return ReloadReport{}, err
		}
		if o.Scope == extension.Project {
			if err := a.agreeToProject(extension.NewItem("plugin", o.Source)); err != nil {
				return ReloadReport{}, err
			}
		}
	}
	return a.refresh(ctx)
}

// RemovePlugin removes the plugin ref names, by name or source, from the
// user's settings or, with project, the project's, then puts the change in
// effect. What the user decided of the plugin, and its data, stay: another
// project may declare it still.
func (a *App) RemovePlugin(ctx context.Context, ref string, project bool) (ReloadReport, error) {
	scope := extension.User
	if project {
		scope = extension.Project
	}
	plugins := a.Plugins()
	i := slices.IndexFunc(plugins, func(pl Plugin) bool { return pl.Scope == scope && pl.Is(ref) })
	if i < 0 {
		return ReloadReport{}, fmt.Errorf("no plugin %q in the %s settings: /plugins lists them", ref, scope)
	}
	pl := plugins[i]
	if err := a.editSettings(scope, func(s *config.Settings) {
		s.Plugins = slices.DeleteFunc(s.Plugins, func(src string) bool { return src == pl.Source })
	}); err != nil {
		return ReloadReport{}, err
	}
	return a.refresh(ctx)
}

// editSettings applies edit to the settings of scope.
func (a *App) editSettings(scope extension.Scope, edit func(*config.Settings)) error {
	if scope == extension.Project {
		return config.EditProjectSettings(a.Trust().Root, edit)
	}
	return config.EditUserSettings(edit)
}

// sweepPluginCache clears the cache of the commits no one runs any longer.
func sweepPluginCache() {
	if err := extension.SweepCache(); err != nil {
		log.Printf("sweep the plugin cache: %v", err)
	}
}
