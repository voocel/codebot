package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/voocel/codebot/internal/extension"
	"github.com/voocel/codebot/internal/extension/plugin"
	"github.com/voocel/codebot/internal/infra/config"
)

// Marketplace is a catalog of plugins; see plugin.Marketplace.
type Marketplace = plugin.Marketplace

// Listing is a plugin a marketplace lists.
type Listing = plugin.Listing

// Marketplaces reads the marketplaces: the user's and the project's own,
// .agents/plugins/marketplace.json in the home and project directories, and
// those the user's settings list, git ones fetched anew. A name is one
// marketplace's: of two of one name, the one read first is kept. What could
// not be read, or was left out, is told in errs.
func (a *App) Marketplaces(ctx context.Context) (out []*Marketplace, errs []error) {
	home, _ := os.UserHomeDir()
	for _, root := range []string{home, a.root} {
		if root == "" {
			continue
		}
		m, problems, err := plugin.ReadMarketplace(root, nil)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if errs = append(errs, problems...); err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, m)
	}
	layers, err := config.Load(a.cwd)
	if err != nil {
		return out, append(errs, err)
	}
	for _, raw := range layers.User.Marketplaces {
		src, err := plugin.ParseSource(raw, extension.PluginBase(extension.User, ""))
		var m *Marketplace
		var problems []error
		if err == nil {
			m, problems, err = readMarketplace(ctx, src)
		}
		if errs = append(errs, problems...); err != nil {
			errs = append(errs, fmt.Errorf("marketplace %s: %w", raw, err))
			continue
		}
		out = append(out, m)
	}
	first := map[string]*Marketplace{}
	return slices.DeleteFunc(out, func(m *Marketplace) bool {
		if f, ok := first[m.Name]; ok {
			errs = append(errs, fmt.Errorf("marketplace %s at %s is left out: the one at %s has its name", m.Name, m.Where, f.Where))
			return true
		}
		first[m.Name] = m
		return false
	}), errs
}

// readMarketplace reads the marketplace at src, a git one fetched anew at
// its ref. One that cannot be fetched is read as it was fetched last, if it
// was, and the problems say so.
func readMarketplace(ctx context.Context, src plugin.Source) (*Marketplace, []error, error) {
	if src.Dir != "" {
		return plugin.ReadMarketplace(src.Dir, nil)
	}
	repo, err := plugin.Mirror(ctx, src, extension.MarketplacesDir())
	var stale error
	if err != nil {
		if repo = plugin.Mirrored(src, extension.MarketplacesDir()); repo == "" {
			return nil, nil, err
		}
		stale = fmt.Errorf("marketplace %s is as fetched before: %w", src, err)
	}
	m, problems, err := plugin.ReadMarketplace(filepath.Join(repo, filepath.FromSlash(src.Path)), &src)
	if stale != nil {
		problems = append(problems, stale)
	}
	return m, problems, err
}

// listed returns the source of the plugin ref names, "<plugin>@<marketplace>".
func (a *App) listed(ctx context.Context, ref string) (string, error) {
	name, market, _ := strings.Cut(ref, "@")
	ms, errs := a.Marketplaces(ctx)
	for _, m := range ms {
		if m.Name != market {
			continue
		}
		for _, l := range m.Plugins {
			switch {
			case l.Name != name:
			case l.Source == "":
				return "", fmt.Errorf("%s: %s", ref, l.Unsupported)
			default:
				return l.Source, nil
			}
		}
		return "", fmt.Errorf("marketplace %s lists no plugin %s", market, name)
	}
	return "", errors.Join(append([]error{fmt.Errorf("no marketplace %s", market)}, errs...)...)
}

// Declares reports whether the settings declare the plugin at source, as a
// marketplace lists it.
func (a *App) Declares(source string) bool {
	src, err := plugin.ParseSource(source, a.cwd)
	return err == nil && slices.ContainsFunc(a.Plugins(), func(pl Plugin) bool {
		declared, err := plugin.ParseSource(pl.Source, extension.PluginBase(pl.Scope, a.root))
		return err == nil && declared == src
	})
}

// isListing reports whether ref names a plugin a marketplace lists,
// "<plugin>@<marketplace>", rather than a source: those hold a "/" or ":"
// where they hold an "@".
func isListing(ref string) bool {
	return strings.Count(ref, "@") == 1 && !strings.ContainsAny(ref, "/:")
}

// AddMarketplace adds the marketplace at source, a git repository or a
// directory, to the user's settings once it reads, and its name is no
// other's. problems are what of it failed to read.
func (a *App) AddMarketplace(ctx context.Context, source string) (m *Marketplace, problems []error, err error) {
	src, err := plugin.ParseSource(source, a.cwd)
	if err != nil {
		return nil, nil, err
	}
	if src.Dir != "" && strings.HasPrefix(source, ".") {
		source = src.Dir
	}
	raw, err := a.userMarketplace(src)
	if err != nil {
		return nil, nil, err
	}
	if raw != "" {
		return nil, nil, fmt.Errorf("%s is added already, as %s", source, raw)
	}
	if m, problems, err = readMarketplace(ctx, src); err != nil {
		return nil, nil, err
	}
	// Those that cannot be read now are no reason to refuse this one.
	others, _ := a.Marketplaces(ctx)
	for _, o := range others {
		if o.Name == m.Name {
			return nil, nil, fmt.Errorf("a marketplace named %s is there already, at %s", m.Name, o.Where)
		}
	}
	return m, problems, config.EditSettings(config.UserSettingsPath(), func(s *config.Settings) { s.Marketplaces = append(s.Marketplaces, source) })
}

// RemoveMarketplace removes the marketplace at source from the user's
// settings, however they spell it.
func (a *App) RemoveMarketplace(source string) error {
	src, err := plugin.ParseSource(source, a.cwd)
	if err != nil {
		return err
	}
	raw, err := a.userMarketplace(src)
	if err != nil {
		return err
	}
	if raw == "" {
		return fmt.Errorf("no marketplace %s in your settings", source)
	}
	return config.EditSettings(config.UserSettingsPath(), func(s *config.Settings) {
		s.Marketplaces = slices.DeleteFunc(s.Marketplaces, func(m string) bool { return m == raw })
	})
}

// userMarketplace returns the user's settings' marketplace at src, as they
// spell it, "" for none.
func (a *App) userMarketplace(src plugin.Source) (string, error) {
	layers, err := config.Load(a.cwd)
	if err != nil {
		return "", err
	}
	for _, raw := range layers.User.Marketplaces {
		if added, err := plugin.ParseSource(raw, extension.PluginBase(extension.User, "")); err == nil && added == src {
			return raw, nil
		}
	}
	return "", nil
}
