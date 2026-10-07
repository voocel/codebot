// Package worktree is a stateless wrapper over `git worktree` for the
// sandboxes the agent works in. The lifecycle lives in app.Conversation.
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/voocel/codebot/internal/infra/config"
)

// branchPrefix lets List find codebot's branches and keeps them apart from
// the user's.
const branchPrefix = "codebot/"

// DefaultIncludes are gitignored files copied into a new worktree, which a
// clean checkout would lack; a missing .env is the usual reason a sandboxed
// build fails.
var DefaultIncludes = []string{".env", ".env.local"}

type Info struct {
	Path   string
	Branch string // "" when detached
}

var nonSlugChars = regexp.MustCompile(`[^a-z0-9._-]+`)

func Slug(name string) string {
	s := nonSlugChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-._")
	if s == "" {
		return "scratch"
	}
	return s
}

// Root is under .codebot/, which is gitignored, so worktrees stay out of the
// user's git status.
func Root(repoRoot string) string {
	return filepath.Join(repoRoot, config.ConfigDir, "worktrees")
}

// Create fails if slug is taken, so the caller can ask for another name.
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
	// `git worktree list` resolves symlinks (macOS /var is /private/var), and
	// orphan cleanup compares paths with its output.
	if resolved, rerr := filepath.EvalSymlinks(dir); rerr == nil {
		dir = resolved
	}
	return dir, branch, nil
}

func HasChanges(dir string) (bool, error) {
	out, err := git(dir, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status: %s", out)
	}
	return strings.TrimSpace(out) != "", nil
}

// Remove without force never loses work: git refuses to remove a dirty
// checkout or delete a branch with unmerged commits. branchKept reports that
// the checkout is gone but the branch stayed because the agent committed to
// it, so the caller can tell the user where the work is.
func Remove(repoRoot, dir, branch string, force bool) (branchKept bool, err error) {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, dir)
	if out, e := git(repoRoot, args...); e != nil {
		return false, fmt.Errorf("git worktree remove: %s", out)
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	if _, e := git(repoRoot, "branch", flag, branch); e != nil && !force {
		return true, nil
	}
	return false, nil
}

// List returns only codebot's worktrees.
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
			// git prints forward slashes even on Windows.
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

// CopyIncludes returns the files it found but could not copy, so the caller
// can warn; err means the lookup itself failed. Missing files are not
// failures.
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

func IsRepo(dir string) bool {
	out, err := git(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

func CurrentBranch(dir string) string {
	out, err := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
