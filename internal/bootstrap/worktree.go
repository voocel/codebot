package bootstrap

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/worktree"
)

// worktreeState tracks the sandbox the session is currently inside.
type worktreeState struct {
	slug   string
	dir    string
	branch string
}

// WorktreeExit reports what ExitWorktree did so the UI can tell the user
// whether changes were kept for review or the sandbox was cleaned up.
type WorktreeExit struct {
	Slug       string
	Dir        string
	Branch     string
	HadChanges bool
	Kept       bool // true when changes were preserved instead of removed
	// BranchKept is true when the checkout was removed but its branch was
	// retained because it held commits not reachable elsewhere — the work
	// survives on the branch even though the working tree was clean.
	BranchKept bool
}

// WorktreeActive reports whether the session is currently inside a sandbox.
func (r *Runtime) WorktreeActive() bool { return r.activeWorktree != nil }

// WorktreeDir returns the active sandbox path, or "" when on the main worktree.
func (r *Runtime) WorktreeDir() string {
	if r.activeWorktree == nil {
		return ""
	}
	return r.activeWorktree.dir
}

// EnterWorktree creates a sandbox worktree and relocates the session into it.
// It is rejected when not in a git repo, when already in a sandbox, or while
// teammates are live (they run against the main cwd and would be split off).
func (r *Runtime) EnterWorktree(name string) (string, error) {
	if r.activeWorktree != nil {
		return "", fmt.Errorf("already in worktree %q — /worktree exit first", r.activeWorktree.slug)
	}
	if !config.IsGitRepo(r.Cwd) {
		return "", fmt.Errorf("worktree requires a git repository")
	}
	if r.hasActiveTeammates() {
		return "", fmt.Errorf("dismiss active teammates before entering a worktree")
	}

	slug := worktree.Slug(name)
	dir, branch, err := worktree.Create(r.Cwd, slug)
	if err != nil {
		return "", err
	}
	// Propagate the local files a clean checkout lacks (.env, ...). A copy
	// failure (rare: a present-but-unreadable file) is surfaced, not swallowed,
	// so a sandbox missing its config doesn't fail mysteriously later.
	if failed, cerr := worktree.CopyIncludes(r.Cwd, dir, worktree.DefaultIncludes); cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: worktree %q: copy local files: %v\n", slug, cerr)
	} else if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "warning: worktree %q could not copy local files: %s\n", slug, strings.Join(failed, ", "))
	}

	r.Session.RetargetWorkspace(dir)
	r.ApprovalEngine.SetFilesystemRoots(rootsWith(r.originalRoots, dir))
	r.activeWorktree = &worktreeState{slug: slug, dir: dir, branch: branch}
	return dir, nil
}

// ExitWorktree returns the session to the main workspace. With uncommitted
// changes it keeps the sandbox for review unless discard is set; a clean (or
// discarded) sandbox is removed along with its branch and snapshot artifacts.
// Removal is data-safe: git refuses to drop a dirty checkout or a branch with
// unmerged commits, so committed work survives even on the clean path.
func (r *Runtime) ExitWorktree(discard bool) (WorktreeExit, error) {
	wt := r.activeWorktree
	if wt == nil {
		return WorktreeExit{}, fmt.Errorf("not in a worktree")
	}
	// Symmetric with EnterWorktree's guard: a teammate spawned while inside the
	// sandbox shares its cwd (carried on the spawn context), so removing the
	// worktree underneath a live teammate would strand it. Make the user dismiss
	// them first.
	if r.hasActiveTeammates() {
		return WorktreeExit{}, fmt.Errorf("dismiss active teammates before exiting the worktree")
	}
	changed, cerr := worktree.HasChanges(wt.dir)
	if cerr != nil {
		// Can't verify the tree is clean — never auto-remove on a guess.
		changed = true
	}

	// Restore the main surface first so the session is off the worktree before
	// we touch its directory.
	r.Session.RetargetWorkspace(r.Cwd)
	r.ApprovalEngine.SetFilesystemRoots(r.originalRoots)
	r.activeWorktree = nil

	res := WorktreeExit{Slug: wt.slug, Dir: wt.dir, Branch: wt.branch, HadChanges: changed}
	if changed && !discard {
		res.Kept = true
		return res, nil
	}
	branchKept, err := worktree.Remove(r.Cwd, wt.dir, wt.branch, discard)
	if err != nil {
		// Removal failed and the sandbox is intact — restore active state so the
		// user can retry /worktree exit|discard instead of being stranded.
		r.Session.RetargetWorkspace(wt.dir)
		r.ApprovalEngine.SetFilesystemRoots(rootsWith(r.originalRoots, wt.dir))
		r.activeWorktree = wt
		return res, err
	}
	res.BranchKept = branchKept
	cleanWorktreeArtifacts(wt.dir)
	return res, nil
}

// formatWorktreeExitForModel renders an ExitWorktree result for the model. Like
// ui.formatWorktreeExit but without the /worktree slash hints (the model exits
// via the tool, not the command).
func formatWorktreeExitForModel(res WorktreeExit) string {
	switch {
	case res.Kept:
		return fmt.Sprintf(
			"Left worktree %q — uncommitted changes kept for review at %s (branch %s). Review or merge with git when done.",
			res.Slug, res.Dir, res.Branch)
	case res.HadChanges:
		return fmt.Sprintf("Left and discarded worktree %q (changes dropped).", res.Slug)
	case res.BranchKept:
		return fmt.Sprintf(
			"Left worktree %q — working tree was clean, but branch %s has commits not merged elsewhere, so the branch was kept.",
			res.Slug, res.Branch)
	default:
		return fmt.Sprintf("Left worktree %q — no changes, cleaned up.", res.Slug)
	}
}

// WorktreeDiff returns the active sandbox's uncommitted diff for review.
func (r *Runtime) WorktreeDiff() (string, error) {
	if r.activeWorktree == nil {
		return "", fmt.Errorf("not in a worktree")
	}
	return worktree.Diff(r.activeWorktree.dir)
}

// CleanWorktreeOrphans removes leftover codebot worktrees with no uncommitted
// changes — sandboxes a previous run created but never cleaned (e.g. after a
// crash). Dirty ones are always kept; unreviewed work is never destroyed.
func (r *Runtime) CleanWorktreeOrphans() {
	if !config.IsGitRepo(r.Cwd) {
		return
	}
	infos, err := worktree.List(r.Cwd)
	if err != nil {
		return
	}
	for _, info := range infos {
		if r.activeWorktree != nil && info.Path == r.activeWorktree.dir {
			continue
		}
		if changed, err := worktree.HasChanges(info.Path); err != nil || changed {
			continue
		}
		// Non-force Remove is data-safe: a branch with unmerged commits keeps its
		// branch (only the clean checkout is dropped), so committed work is never
		// lost even though HasChanges sees only the working tree.
		branch := strings.TrimPrefix(info.Branch, "refs/heads/")
		if _, err := worktree.Remove(r.Cwd, info.Path, branch, false); err != nil {
			continue
		}
		cleanWorktreeArtifacts(info.Path)
	}
}

// hasActiveTeammates reports whether any spawned teammate is still live. The
// leader is never in the event hub, so iterating registered names and checking
// the hub naturally excludes it.
func (r *Runtime) hasActiveTeammates() bool {
	if r.TeamRegistry == nil || r.TeammateEvents == nil {
		return false
	}
	return slices.ContainsFunc(r.TeamRegistry.AgentNames(), r.TeammateEvents.IsActive)
}

// rootsWith returns base with dir added to the read and write roots, so the
// sandbox is accessible while every other allowed path (sessions, memory,
// plans) is preserved.
func rootsWith(base approval.FilesystemRoots, dir string) approval.FilesystemRoots {
	out := base
	out.ReadRoots = append(append([]string{}, base.ReadRoots...), dir)
	out.WriteRoots = append(append([]string{}, base.WriteRoots...), dir)
	return out
}

// cleanWorktreeArtifacts removes the per-worktree snapshot shadow repo and undo
// sidecar, which live under ~/.codebot keyed by the worktree's cwd and do not
// disappear when the worktree directory is removed.
func cleanWorktreeArtifacts(dir string) {
	_ = os.RemoveAll(config.SnapshotDir(dir))
	_ = os.RemoveAll(config.SessionsDir(dir))
}
