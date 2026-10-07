package plugin

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/voocel/codebot/internal/lib/detached"
)

type Source struct {
	Dir  string // "" for a git plugin
	URL  string
	Path string // slash-separated directory in the repository; "" for its root
	Ref  string // tag, branch or commit; "" for the default branch
}

// reSCP matches git's scp-like ssh address, user@host:path.
var reSCP = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^/]`)

// reHost requires a dot, so "example.com/owner/repo" names a host but
// "dir/owner/repo" does not.
var reHost = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+(:[0-9]+)?$`)

// ParseSource accepts a path (absolute, ~/ or relative to base) or a git
// repository ("host/owner/repo", or an https or ssh URL). In a repository,
// "//dir" selects the plugin directory and "#ref" pins a tag, branch or
// commit.
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

// cutPath splits at the "//" after the scheme's "://".
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

// String is the canonical form; consents are keyed by it.
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

// key identifies the repository the same way whichever URL form names it.
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

// Cached returns cache/<repository>/<commit>, shared by all plugins in the
// repository.
func Cached(s Source, cache, commit string) string {
	key := s.key()
	return filepath.Join(cache, named(path.Base(key), key), commit)
}

// DataDir ignores the ref, so the data survives updates.
func DataDir(s Source, root string) string {
	hint := filepath.Base(s.Dir)
	if s.Dir == "" {
		hint = path.Base(cmp.Or(s.Path, s.key()))
	}
	s.Ref = ""
	return filepath.Join(root, named(hint, s.String()))
}

// named joins a readable hint with a digest of id, so no two ids share a
// directory. It never starts with a dot, which marks a fetch in progress.
func named(hint, id string) string {
	sum := sha256.Sum256([]byte(id))
	return strings.TrimLeft(hint, ".") + "-" + hex.EncodeToString(sum[:4])
}

// ReadCached touches the checkout so SweepCache keeps it, and rejects a
// plugin directory that resolves outside the checkout.
func ReadCached(s Source, cache, commit, data string) (*Plugin, []error, error) {
	repo := Cached(s, cache, commit)
	now := time.Now()
	if err := os.Chtimes(repo, now, now); err != nil {
		return nil, nil, err
	}
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
	return Read(dir, data)
}

// Fetch fetches commit, or the source's ref when commit is "", and returns
// the commit. A cached commit is not fetched again, and a failed fetch
// leaves nothing behind.
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
	// Another fetch, possibly in another process, may have cached it first.
	if err := os.Rename(tmp, dir); err != nil {
		if _, statErr := os.Stat(dir); statErr != nil {
			return "", err
		}
	}
	return got, nil
}

// unusedAge leaves room for sessions still running a commit they read
// earlier.
const unusedAge = 14 * 24 * time.Hour

// SweepCache skips dot-named entries, which are fetches in progress.
func SweepCache(cache string, now time.Time) error {
	repos, err := os.ReadDir(cache)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, r := range repos {
		if !r.IsDir() || strings.HasPrefix(r.Name(), ".") {
			continue
		}
		repo := filepath.Join(cache, r.Name())
		commits, err := os.ReadDir(repo)
		if err != nil {
			return err
		}
		left := len(commits)
		for _, c := range commits {
			info, err := c.Info()
			if err != nil {
				return err
			}
			if now.Sub(info.ModTime()) >= unusedAge {
				if err := os.RemoveAll(filepath.Join(repo, c.Name())); err != nil {
					return err
				}
				left--
			}
		}
		if left == 0 {
			if err := os.Remove(repo); err != nil {
				return err
			}
		}
	}
	return nil
}

// Latest resolves the ref as Fetch would, with ls-remote and no fetch. It
// returns "" when the remote has no such ref, as when the ref is a commit.
// It runs git in cache, outside any repository.
func Latest(ctx context.Context, s Source, cache string) (string, error) {
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	want := cmp.Or(s.Ref, "HEAD")
	out, err := git(ctx, cache, "ls-remote", "--", s.URL, want, want+"^{}")
	if err != nil {
		return "", err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if commit, ref, ok := strings.Cut(line, "\t"); ok {
			refs[ref] = commit
		}
	}
	// The order git fetch resolves refs in; a tag resolves to its commit.
	for _, f := range []string{"%s", "refs/%s", "refs/tags/%s", "refs/heads/%s", "refs/remotes/%s", "refs/remotes/%s/HEAD"} {
		ref := fmt.Sprintf(f, want)
		if commit := cmp.Or(refs[ref+"^{}"], refs[ref]); commit != "" {
			return commit, nil
		}
	}
	return "", nil
}

// git allows only https and ssh and never prompts: it uses whatever
// credentials the user's git config and ssh agent already hold. Running
// detached stops ssh from asking for a passphrase or host key, and
// cancellation also kills git's helpers (git-remote-https, ssh).
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := detached.Command(ctx, "git", append([]string{
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always",
		"-c", "core.hooksPath=" + os.DevNull,
	}, args...)...)
	cmd.Dir = dir
	// Skip LFS: the repository's .lfsconfig could point git-lfs anywhere, and
	// plugins don't need large files.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "SSH_ASKPASS_REQUIRE=never")
	out, err := cmd.Output()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return "", errors.New(msg)
		}
	}
	return strings.TrimSpace(string(out)), err
}
