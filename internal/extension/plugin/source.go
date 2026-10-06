package plugin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Source is where a plugin comes from: a directory, or a git repository at
// a ref, the plugin in it or in one of its directories.
type Source struct {
	Dir  string // a local plugin's directory; "" for a git one
	URL  string // a git repository's
	Path string // the plugin's directory in the repository, slashed; "" for its root
	Ref  string // the tag, branch or commit to take; "" for the default branch
}

// reSCP matches git's scp-like ssh address, user@host:path.
var reSCP = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^/]`)

// reHost matches a host's name with a dot in it, as "host/owner/repo"
// starts: example.com, not .codebot or a directory's name.
var reHost = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+(:[0-9]+)?$`)

// ParseSource parses a plugin's source as settings declare it: a path,
// absolute or relative to base, or a git repository, "host/owner/repo" or
// an https or ssh URL, "//dir" naming the plugin's directory in it and
// "#ref" pinning a tag, branch or commit.
func ParseSource(raw, base string) (Source, error) {
	raw = strings.TrimSpace(raw)
	if dir, ok := localDir(raw, base); ok {
		return Source{Dir: dir}, nil
	}
	repo, ref, _ := strings.Cut(raw, "#")
	repo, sub, ok := cutPath(repo)
	repo = strings.TrimSuffix(repo, "/")
	if ok && (sub == "" || path.IsAbs(sub) || path.Clean(sub) != sub || sub == ".." || strings.HasPrefix(sub, "../") || strings.Contains(sub, "\\")) {
		return Source{}, fmt.Errorf("plugin source %q names no directory in the repository", raw)
	}
	switch {
	case strings.HasPrefix(repo, "https://") || strings.HasPrefix(repo, "ssh://"):
		u, err := url.Parse(repo)
		if err != nil || u.Host == "" || strings.Trim(u.Path, "/") == "" {
			return Source{}, fmt.Errorf("plugin source %q is not a repository URL", raw)
		}
	case reSCP.MatchString(repo):
	case strings.Count(repo, "/") >= 2 && reHost.MatchString(strings.Split(repo, "/")[0]):
		repo = "https://" + repo
	default:
		return Source{}, fmt.Errorf("plugin source %q is neither a git repository nor a path", raw)
	}
	if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, " \t\n") {
		return Source{}, fmt.Errorf("plugin source %q has an invalid ref", raw)
	}
	return Source{URL: repo, Path: sub, Ref: ref}, nil
}

// cutPath cuts repo at the "//" that names a directory in it, past the
// scheme's.
func cutPath(repo string) (before, after string, found bool) {
	start := 0
	if i := strings.Index(repo, "://"); i >= 0 {
		start = i + len("://")
	}
	before, after, found = strings.Cut(repo[start:], "//")
	return repo[:start] + before, after, found
}

func localDir(raw, base string) (string, bool) {
	switch {
	case raw == "~" || strings.HasPrefix(raw, "~/"):
		home, _ := os.UserHomeDir()
		return filepath.Join(home, raw[1:]), true
	case filepath.IsAbs(raw):
		return filepath.Clean(raw), true
	case raw == "." || raw == ".." || strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../"):
		return filepath.Join(base, raw), true
	}
	return "", false
}

// String tells the source as it is fetched and locked: the directory, or
// the repository, the plugin's directory in it and the ref.
func (s Source) String() string {
	if s.Dir != "" {
		return s.Dir
	}
	out := s.URL
	if s.Path != "" {
		out += "//" + s.Path
	}
	if s.Ref != "" {
		out += "#" + s.Ref
	}
	return out
}

// key is where a git source's commits are cached: under its host and path.
func (s Source) key() string {
	repo := s.URL
	if reSCP.MatchString(repo) {
		_, addr, _ := strings.Cut(repo, "@")
		host, p, _ := strings.Cut(addr, ":")
		repo = host + "/" + p
	} else if u, err := url.Parse(repo); err == nil {
		repo = u.Hostname() + u.Path
	}
	repo = path.Clean("/" + strings.TrimSuffix(repo, ".git"))
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == '/':
			return r
		}
		return '_'
	}, strings.TrimPrefix(repo, "/"))
}

// Cached returns the directory the cache under cache keeps the commit of a
// git source's repository in: the plugins of one repository share it.
func Cached(s Source, cache, commit string) string {
	return filepath.Join(cache, filepath.FromSlash(s.key()), commit)
}

// ReadCached reads the plugin of the git source s from the checkout of its
// commit under cache, which the plugin's directory may not lead out of.
func ReadCached(s Source, cache, commit, dataRoot string) (*Plugin, []error, error) {
	repo := Cached(s, cache, commit)
	dir := filepath.Join(repo, filepath.FromSlash(s.Path))
	realRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return nil, nil, err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, nil, err
	}
	if !within(realRepo, real) {
		return nil, nil, fmt.Errorf("%s leads outside %s", s.Path, s.URL)
	}
	return Read(dir, dataRoot)
}

// Fetch fetches from git into the cache under cache the commit given, or
// where it is "", the commit at the source's ref; it returns the commit. A
// commit cached already is not fetched again. It runs the user's git, with
// their credentials, over https or ssh alone, and never prompts. A fetch
// that fails leaves nothing behind.
func Fetch(ctx context.Context, s Source, cache, commit string) (string, error) {
	want := commit
	if want == "" {
		want = cmp.Or(s.Ref, "HEAD")
	} else if _, err := os.Stat(Cached(s, cache, commit)); err == nil {
		return commit, nil
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(cache, ".fetch-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	for _, args := range [][]string{
		{"init", "-q"},
		{"fetch", "-q", "--depth", "1", "--no-tags", "--", s.URL, want},
		{"checkout", "-q", "--detach", "FETCH_HEAD"},
	} {
		if _, err := git(ctx, tmp, args...); err != nil {
			if strings.Contains(err.Error(), "could not read Username") {
				err = errors.New("no such repository, or git has no credentials for it")
			}
			if commit != "" {
				return "", fmt.Errorf("fetch %s at %s: %w", s, commit, err)
			}
			return "", fmt.Errorf("fetch %s: %w", s, err)
		}
	}
	got, err := git(ctx, tmp, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if commit != "" && got != commit {
		return "", fmt.Errorf("fetch %s at %s: got %s", s, commit, got)
	}
	if err := os.RemoveAll(filepath.Join(tmp, ".git")); err != nil {
		return "", err
	}
	dir := Cached(s, cache, got)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	// Another fetch, of this codebot or another, may have cached it first.
	if err := os.Rename(tmp, dir); err != nil {
		if _, statErr := os.Stat(dir); statErr != nil {
			return "", err
		}
	}
	return got, nil
}

// orphanAge is how long a commit no longer locked stays cached: a session
// that loaded it before may run it still.
const orphanAge = 14 * 24 * time.Hour

// reCommit matches a commit's name, SHA-1 or SHA-256.
var reCommit = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Sweep clears the cache under cache of the checkouts keep does not hold,
// by directory. It marks one it finds unheld, beside it, and removes it
// once it was marked orphanAge ago; one held again loses its mark.
func Sweep(cache string, keep map[string]bool, now time.Time) error {
	err := filepath.WalkDir(cache, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case p == cache || !d.IsDir():
			return nil
		case strings.HasPrefix(d.Name(), "."):
			return filepath.SkipDir // a fetch under way
		case !reCommit.MatchString(d.Name()):
			return nil // a repository's path
		}
		marker := p + ".orphaned"
		marked, err := os.Stat(marker)
		switch {
		case keep[p]:
			err = os.Remove(marker)
		case errors.Is(err, fs.ErrNotExist):
			err = os.WriteFile(marker, nil, 0o600)
		case err == nil && now.Sub(marked.ModTime()) >= orphanAge:
			if err = os.RemoveAll(p); err == nil {
				err = os.Remove(marker)
			}
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return filepath.SkipDir
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// git runs git in dir and returns its output, trimmed. Of the transports
// it takes https and ssh alone, nor does it ask for credentials: what the
// user's git, as they set it up, and ssh agent hold is all it has. Nothing
// it runs may prompt on the terminal the TUI draws on.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always",
		"-c", "core.hooksPath=" + os.DevNull,
	}, args...)...)
	cmd.Dir = dir
	// The user's git-lfs would fetch large files from where the repository's
	// .lfsconfig says; a plugin has no business with them.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "SSH_ASKPASS_REQUIRE=never")
	detach(cmd)
	out, err := cmd.Output()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return "", errors.New(msg)
		}
	}
	return strings.TrimSpace(string(out)), err
}
