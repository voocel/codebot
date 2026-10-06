package plugin

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/voocel/codebot/internal/lib/regular"
)

// MarketplaceFile is where a marketplace lies under its root, as Codex
// keeps it.
const MarketplaceFile = ".agents/plugins/marketplace.json"

// Marketplace is a catalog of plugins in Codex's format: a name, and the
// plugins it lists with where they come from.
type Marketplace struct {
	Name  string
	Title string // its display name, its name where it gives none
	// Where is where it was read from: a directory, or a git source.
	Where   string
	Plugins []Listing
}

// Listing is a plugin a marketplace lists.
type Listing struct {
	Name        string
	Description string
	Category    string
	// Source is where the plugin comes from, as settings are to declare
	// it; "" where codebot cannot fetch it, Unsupported saying why.
	Source      string
	Unsupported string
}

// ReadMarketplace reads the marketplace under root, at MarketplaceFile.
// The plugins it lists in its own directories are in repo where it was
// fetched from git, in root where it is local, repo nil. A listing that
// breaks the format is left out and reported among problems.
func ReadMarketplace(root string, repo *Source) (m *Marketplace, problems []error, err error) {
	file := filepath.Join(root, filepath.FromSlash(MarketplaceFile))
	data, err := regular.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	var raw struct {
		Name      string `json:"name"`
		Interface struct {
			DisplayName string `json:"displayName"`
		} `json:"interface"`
		Plugins []json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	if raw.Name == "" || raw.Plugins == nil {
		return nil, nil, fmt.Errorf("%s: a marketplace needs a name and plugins", file)
	}
	m = &Marketplace{Name: raw.Name, Title: raw.Interface.DisplayName, Where: root}
	if m.Title == "" {
		m.Title = m.Name
	}
	if repo != nil {
		m.Where = repo.String()
	}
	for i, p := range raw.Plugins {
		l, ok, err := listing(p, root, repo)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: plugins[%d]: %w", file, i, err))
			continue
		}
		if ok {
			m.Plugins = append(m.Plugins, l)
		}
	}
	return m, problems, nil
}

// listing reads a plugin a marketplace lists; ok is false for one it holds
// back from installing.
func listing(raw json.RawMessage, root string, repo *Source) (l Listing, ok bool, err error) {
	var p struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Category    string          `json:"category"`
		Source      json.RawMessage `json:"source"`
		Policy      struct {
			Installation string `json:"installation"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return Listing{}, false, err
	}
	if p.Name == "" || p.Source == nil {
		return Listing{}, false, fmt.Errorf("a plugin needs a name and a source")
	}
	if p.Policy.Installation == "NOT_AVAILABLE" {
		return Listing{}, false, nil
	}
	l = Listing{Name: p.Name, Description: p.Description, Category: p.Category}
	l.Source, err = listedSource(p.Source, root, repo)
	if u, is := errors.AsType[unsupported](err); is {
		l.Unsupported, err = u.Error(), nil
	}
	return l, err == nil, err
}

// unsupported is a source in the format that codebot cannot fetch.
type unsupported struct{ error }

// listedSource is where a listing's source says the plugin is, as settings
// are to declare it: "./dir" or {"source": "local", "path"} in the
// marketplace's own directories; {"source": "url", "url", "path", "ref",
// "sha"} or {"source": "git-subdir", ...} in a git repository, its url a
// GitHub "owner/repo" or an https or ssh URL. A sha pins the commit over a
// ref.
func listedSource(raw json.RawMessage, root string, repo *Source) (string, error) {
	var s struct {
		Source  string `json:"source"`
		Path    string `json:"path"`
		URL     string `json:"url"`
		Ref     string `json:"ref"`
		SHA     string `json:"sha"`
		Package string `json:"package"`
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte(`"`)) {
		s.Source = "local"
		if err := json.Unmarshal(raw, &s.Path); err != nil {
			return "", err
		}
	} else if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	switch s.Source {
	case "local":
		dir, err := inRoot(s.Path, true)
		if err != nil {
			return "", err
		}
		if repo == nil {
			return filepath.Join(root, filepath.FromSlash(dir)), nil
		}
		in := *repo
		in.Path = path.Join(repo.Path, dir)
		return in.String(), nil
	case "url", "git-subdir":
		var dir string
		if s.Source == "git-subdir" || s.Path != "" {
			var err error
			if dir, err = inRoot(s.Path, false); err != nil {
				return "", err
			}
		}
		repoURL := strings.TrimSpace(s.URL)
		switch {
		case strings.HasPrefix(repoURL, "https://"), strings.HasPrefix(repoURL, "ssh://"), reSCP.MatchString(repoURL):
		case reGitHub.MatchString(repoURL):
			repoURL = "github.com/" + repoURL
		default:
			return "", unsupported{fmt.Errorf("codebot fetches plugins over https and ssh, not from %q", s.URL)}
		}
		src := Source{URL: repoURL, Path: dir, Ref: cmp.Or(s.SHA, s.Ref)}
		if _, err := ParseSource(src.String(), ""); err != nil {
			return "", err
		}
		return src.String(), nil
	case "npm":
		return "", unsupported{fmt.Errorf("codebot does not install npm packages, such as %s", s.Package)}
	}
	return "", fmt.Errorf("unknown source %q", s.Source)
}

// reGitHub matches GitHub's "owner/repo".
var reGitHub = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// inRoot checks dir, a directory under a marketplace's or a repository's
// root: slashed, with no "..". Local ones start "./"; "." or "./" is the
// root itself where root may be named.
func inRoot(dir string, local bool) (string, error) {
	rel := strings.TrimSpace(dir)
	if local {
		if rel == "." || rel == "./" {
			return ".", nil
		}
		var ok bool
		if rel, ok = strings.CutPrefix(rel, "./"); !ok {
			return "", fmt.Errorf("local path %q does not start ./", dir)
		}
	} else {
		rel = strings.TrimPrefix(rel, "./")
	}
	if rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, `\`) {
		return "", fmt.Errorf("path %q leads outside the root", dir)
	}
	return rel, nil
}
