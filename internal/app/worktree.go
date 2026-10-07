package app

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/workspace/worktree"
)

type worktreeState struct {
	slug   string
	dir    string
	branch string
}

type WorktreeExit struct {
	Slug       string
	Dir        string
	Branch     string
	HadChanges bool
	Kept       bool // changes kept for review instead of removed
	// BranchKept means the clean checkout was removed but its branch was
	// kept, because it holds commits not reachable elsewhere.
	BranchKept bool
}

func (r WorktreeExit) forModel() string {
	switch {
	case r.Kept:
		return fmt.Sprintf("Left worktree %q — uncommitted changes kept for review at %s (branch %s). Review or merge with git when done.", r.Slug, r.Dir, r.Branch)
	case r.HadChanges:
		return fmt.Sprintf("Left and discarded worktree %q (changes dropped).", r.Slug)
	case r.BranchKept:
		return fmt.Sprintf("Left worktree %q — working tree was clean, but branch %s has commits not merged elsewhere, so the branch was kept.", r.Slug, r.Branch)
	default:
		return fmt.Sprintf("Left worktree %q — no changes, cleaned up.", r.Slug)
	}
}

func (c *Conversation) Worktree() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.worktree == nil {
		return ""
	}
	return c.worktree.dir
}

func (c *Conversation) EnterWorktree(name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.worktree != nil {
		return "", fmt.Errorf("already in worktree %q — /worktree exit first", c.worktree.slug)
	}
	root := c.app.cwd
	if !worktree.IsRepo(root) {
		return "", fmt.Errorf("worktree requires a git repository")
	}
	slug := worktree.Slug(name)
	dir, branch, err := worktree.Create(root, slug)
	if err != nil {
		return "", err
	}
	// A fresh checkout lacks untracked local files such as .env. Report the
	// ones that fail to copy so the sandbox doesn't fail mysteriously later.
	if failed, err := worktree.CopyIncludes(root, dir, worktree.DefaultIncludes); err != nil {
		log.Printf("worktree %q: copy local files: %v", slug, err)
	} else if len(failed) > 0 {
		log.Printf("worktree %q: could not copy local files: %s", slug, strings.Join(failed, ", "))
	}

	c.worktree = &worktreeState{slug: slug, dir: dir, branch: branch}
	c.moveLocked(dir)
	return dir, nil
}

// ExitWorktree keeps a sandbox with uncommitted changes for review unless
// discard is set; otherwise it removes the sandbox and its branch. git
// refuses to drop a branch with unmerged commits.
func (c *Conversation) ExitWorktree(discard bool) (WorktreeExit, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	wt := c.worktree
	if wt == nil {
		return WorktreeExit{}, fmt.Errorf("not in a worktree")
	}
	changed, err := worktree.HasChanges(wt.dir)
	if err != nil {
		changed = true // never remove what could not be checked
	}
	res := WorktreeExit{Slug: wt.slug, Dir: wt.dir, Branch: wt.branch, HadChanges: changed}
	if !changed || discard {
		// Remove before leaving, so on failure the conversation stays in the
		// intact sandbox and the user can retry.
		if res.BranchKept, err = worktree.Remove(c.app.cwd, wt.dir, wt.branch, discard); err != nil {
			return res, err
		}
		cleanWorktreeArtifacts(wt.dir)
	} else {
		res.Kept = true
	}
	c.worktree = nil
	c.moveLocked(c.app.cwd)
	return res, nil
}

func (c *Conversation) moveLocked(dir string) {
	c.cwd = dir
	if c.snapshots != nil {
		c.snapshots.Rebind(config.SnapshotDir(dir), dir, config.UndoStatePath(dir, c.id))
	}
	c.skills, c.workspace = c.app.workspace(dir)
	// Tools see the new cwd at once through the run's context; the model is
	// told when the next run starts.
	c.configureLocked()
}

// cleanWorktreeOrphans removes clean worktrees left by a crashed process.
// Dirty ones stay, so unreviewed work is never destroyed.
func cleanWorktreeOrphans(root string) {
	if !worktree.IsRepo(root) {
		return
	}
	infos, err := worktree.List(root)
	if err != nil {
		return
	}
	for _, info := range infos {
		if changed, err := worktree.HasChanges(info.Path); err != nil || changed {
			continue
		}
		// Non-forced removal keeps a branch with unmerged commits.
		branch := strings.TrimPrefix(info.Branch, "refs/heads/")
		if _, err := worktree.Remove(root, info.Path, branch, false); err != nil {
			continue
		}
		cleanWorktreeArtifacts(info.Path)
	}
}

func cleanWorktreeArtifacts(dir string) {
	_ = os.RemoveAll(config.SnapshotDir(dir))
	_ = os.RemoveAll(config.SessionsDir(dir))
}
