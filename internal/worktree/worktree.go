// Package worktree manages ephemeral git worktrees used as isolated sandboxes:
// the agent works inside one, and changes are reviewed and merged or discarded
// on exit. It is a thin, stateless wrapper over `git worktree` — lifecycle and
// session wiring live in the caller (app.Conversation).
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/voocel/codebot/internal/config"
)

// branchPrefix namespaces every codebot-created branch so List/cleanup can find
// them and they never collide with the user's own branches.
const branchPrefix = "codebot/"

// DefaultIncludes are the gitignored files copied into a fresh worktree so it
// can actually run: a clean checkout omits everything git ignores, and a
// missing .env is the most common reason a sandboxed build/test fails.
var DefaultIncludes = []string{".env", ".env.local"}

// Info is one entry from `git worktree list`.
type Info struct {
	Path   string
	Branch string // refs/heads/... or "" when detached
}

var nonSlugChars = regexp.MustCompile(`[^a-z0-9._-]+`)

// Slug normalizes a user-supplied name into a filesystem- and ref-safe slug.
// Empty input yields "scratch" so `/worktree` with no name still works.
func Slug(name string) string {
	s := nonSlugChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-._")
	if s == "" {
		return "scratch"
	}
	return s
}

// Root is the directory the worktrees live in: <repoRoot>/.codebot/worktrees.
// It lives under .codebot/ (already gitignored), so the checkouts never
// pollute the user's status.
func Root(repoRoot string) string {
	return filepath.Join(repoRoot, config.ConfigDir, "worktrees")
}

// Create adds a worktree at <Root>/<slug> on a new branch codebot/<slug>,
// based on the repo's current HEAD. It fails if the slug is already in use so
// the caller can ask for a different name.
func Create(repoRoot, slug string) (dir, branch string, err error) {
	dir = filepath.Join(Root(repoRoot), slug)
	branch = branchPrefix + slug
	if _, statErr := os.Stat(dir); statErr == nil {
		return "", "", fmt.Errorf("worktree %q already exists", slug)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", "", err
	}
	if out, runErr := git(repoRoot, "worktree", "add", "-b", branch, dir); runErr != nil {
		return "", "", fmt.Errorf("git worktree add: %s", out)
	}
	// Normalize so the path matches `git worktree list` output, which resolves
	// symlinks (e.g. macOS /var -> /private/var); the activeWorktree path is
	// later compared against that output in CleanWorktreeOrphans.
	if resolved, rerr := filepath.EvalSymlinks(dir); rerr == nil {
		dir = resolved
	}
	return dir, branch, nil
}

// HasChanges reports whether the worktree has any uncommitted changes (tracked
// or untracked).
func HasChanges(dir string) (bool, error) {
	out, err := git(dir, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status: %s", out)
	}
	return strings.TrimSpace(out) != "", nil
}

// Remove deletes the worktree and its branch. With force it discards everything
// (worktree --force + branch -D) — the caller asked to throw the work away.
// Without force it is data-safe by construction: `git worktree remove` refuses a
// dirty checkout (uncommitted or untracked files), and `git branch -d` refuses a
// branch with commits not reachable from any other ref. So a sandbox the agent
// committed into keeps its branch even when the working tree is clean. The
// returned branchKept reports exactly that case (worktree gone, branch retained
// because it held unmerged commits) so the caller can tell the user where the
// work survived.
func Remove(repoRoot, dir, branch string, force bool) (branchKept bool, err error) {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, dir)
	if out, e := git(repoRoot, args...); e != nil {
		return false, fmt.Errorf("git worktree remove: %s", out)
	}
	flag := "-d" // safe delete: refuses branches with unmerged commits
	if force {
		flag = "-D" // force delete: caller is discarding
	}
	if _, e := git(repoRoot, "branch", flag, branch); e != nil && !force {
		// -d refused: the branch carries commits no other ref reaches. The
		// checkout is gone (it was clean) but the commits live on; keep the
		// branch and signal the caller to surface it.
		return true, nil
	}
	return false, nil
}

// List returns every registered worktree under codebot's namespace, used to
// detect and clean orphans on startup.
func List(repoRoot string) ([]Info, error) {
	out, err := git(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %s", out)
	}
	var infos []Info
	var cur Info
	flush := func() {
		if cur.Path != "" && strings.HasPrefix(cur.Branch, "refs/heads/"+branchPrefix) {
			infos = append(infos, cur)
		}
		cur = Info{}
	}
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			// git prints forward slashes even on Windows; normalize to the
			// OS-native form so Path compares equal to filepath-built paths
			// (CleanWorktreeOrphans).
			cur.Path = filepath.FromSlash(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(line, "branch ")
		case line == "":
			flush()
		}
	}
	flush()
	return infos, nil
}

// CopyIncludes copies gitignored files matching patterns from repoRoot into the
// fresh worktree, so it has the local files (.env, etc.) it needs to actually
// run — a clean checkout omits everything git ignores. It returns the relative
// paths it found but could NOT copy (e.g. a permission error), so the caller can
// warn rather than leave the sandbox silently missing config; err is reserved
// for the lookup itself failing. A file absent from the source is simply not
// listed, never a failure.
func CopyIncludes(repoRoot, dir string, patterns []string) (failed []string, err error) {
	args := append([]string{"ls-files", "--others", "--ignored", "--exclude-standard", "--"}, patterns...)
	out, lerr := git(repoRoot, args...)
	if lerr != nil {
		return nil, fmt.Errorf("git ls-files: %s", out)
	}
	for rel := range strings.SplitSeq(out, "\n") {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		if cerr := copyFile(filepath.Join(repoRoot, rel), filepath.Join(dir, rel)); cerr != nil {
			failed = append(failed, rel)
		}
	}
	return failed, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// IsRepo reports whether dir is inside a git working tree.
func IsRepo(dir string) bool {
	out, err := git(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// CurrentBranch returns the branch checked out in dir, "" outside a
// repository.
func CurrentBranch(dir string) string {
	out, err := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// git runs a git command in dir and returns combined output trimmed.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
