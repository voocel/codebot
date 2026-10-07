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

type Plugin = extension.Plugin

type PluginContent = plugin.Plugin

const (
	PluginOn           = extension.PluginOn
	PluginShadowed     = extension.PluginShadowed
	PluginUntrusted    = extension.PluginUntrusted
	PluginNotInstalled = extension.PluginNotInstalled
	PluginNotCached    = extension.PluginNotCached
	PluginWaiting      = extension.PluginWaiting
	PluginBroken       = extension.PluginBroken
)

// Plugins lists command-line plugins first, then those declared in
// settings, the project's before the user's.
func (a *App) Plugins() []Plugin { return a.Extensions().Plugins }

// PluginOffer is a plugin awaiting the user's consent: a new plugin, a
// declared one that runs something not agreed to, or a newer commit of a git
// plugin. It is taken or left as a whole.
type PluginOffer struct {
	// Source is the plugin's source as written in the settings of Scope.
	Source string
	Scope  extension.Scope
	Commit string
	*plugin.Plugin
	// Surface is everything the plugin runs; New is the part the user has
	// not agreed to.
	Surface, New Surface
	// Problems are the parts that failed to load and were left out.
	Problems []error

	src plugin.Source
	// declare means accepting adds the plugin to the settings.
	declare bool
}

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
	return &PluginOffer{Commit: got, Plugin: p, Surface: surface, New: surface.Missing(consents.Plugins[src.ID()].Surface), Problems: problems, src: src}, nil
}

// OfferPlugin reads the plugin at source: a git repository at its ref, or a
// directory relative to the working directory. AcceptPlugin then adds it to
// the user's settings, or the project's if project is set.
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

// declare returns how the settings of scope should write source. A
// directory inside the project is written relative to the project's
// settings so it works wherever the repository is checked out; another
// relative path becomes absolute; anything else stays as given.
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

// InstallPlugins readies declared plugins. An agreed git plugin missing from
// the cache is fetched at its commit. A plugin not yet agreed to, or running
// something new, is returned as an offer. The extensions reload if anything
// was fetched.
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
		case pl.State == PluginWaiting:
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

type PluginUpdate struct {
	Plugin Plugin
	// Commit equals the plugin's own commit when it is up to date.
	Commit string
	// Offer is set when the new commit runs something the user has not
	// agreed to; the plugin stays on its old commit until AcceptPlugin.
	// When nil, the new commit was applied.
	Offer *PluginOffer
	Err   error
}

// UpdatePlugins fetches agreed git plugins at their refs, or only the one
// ref names. An update that runs nothing new applies at once; the others
// wait for the user's consent.
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
			// A commit missing from the cache takes effect once fetched.
			applied = applied || pl.State == PluginNotCached
		case len(o.New) > 0:
			o.Source, o.Scope = pl.Source, pl.Scope
			u.Commit, u.Offer = o.Commit, o
		default:
			u.Commit, u.Err = o.Commit, extension.AgreeToPlugin(pl.Src, o.Commit, o.Surface)
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

// PluginUpdates names the git plugins whose ref has moved past the agreed
// commit. It only queries the remotes, without fetching; a remote that does
// not answer is skipped silently.
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

// AcceptPlugin agrees to everything the plugin runs at the offered commit,
// then declares the plugin in the settings if needed. A plugin the user adds
// to the project is also trusted as a project item, since they wrote it
// there.
func (a *App) AcceptPlugin(ctx context.Context, o *PluginOffer) (ReloadReport, error) {
	if err := extension.AgreeToPlugin(o.src, o.Commit, o.Surface); err != nil {
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

// RemovePlugin removes the plugin, by name or source, from the user's or
// the project's settings. Its consent and data stay, since another project
// may still declare it.
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

func (a *App) editSettings(scope extension.Scope, edit func(*config.Settings)) error {
	if scope == extension.Project {
		return config.EditProjectSettings(a.Trust().Root, edit)
	}
	return config.EditUserSettings(edit)
}

func sweepPluginCache() {
	if err := extension.SweepCache(); err != nil {
		log.Printf("sweep the plugin cache: %v", err)
	}
}
