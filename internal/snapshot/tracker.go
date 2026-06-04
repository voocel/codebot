// Package snapshot captures workspace file checkpoints into a shadow git
// repository and restores them on demand, powering /undo.
//
// The approach follows opencode's shadow-repo model: a standalone git --git-dir
// (outside the user's project) with the workspace as its --work-tree. Snapshots
// are full-workspace tree objects (git write-tree), so they capture changes
// from any source — write/edit tools, bash (sed -i), even manual edits — and
// never touch the user's own .git state. git's content addressing dedups
// unchanged blobs across snapshots.
package snapshot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// maxFileSize caps which untracked files enter a snapshot. Larger files are
// excluded so the shadow repo doesn't balloon on build artifacts or binaries
// that escaped .gitignore. Mirrors opencode's 2 MiB limit.
const maxFileSize = 2 * 1024 * 1024

// Tracker snapshots the workspace into a shadow git repo and reverts to those
// snapshots. Snapshots are turn-scoped: the session calls Track at each turn
// boundary, pushing the pre-turn tree hash onto stack (turns that change
// nothing are skipped — identical content yields an identical write-tree hash).
// Undo pops the top hash and reverts the workspace to it, undoing the most
// recent turn's file changes.
type Tracker struct {
	git         gitRunner
	mu          sync.Mutex
	stack       []string
	initialized bool
}

// New returns a Tracker writing snapshots to gitDir for the given workspace.
func New(gitDir, workTree string) *Tracker {
	return &Tracker{git: gitRunner{gitDir: gitDir, workTree: workTree}}
}

// Track snapshots the current workspace as the start point of the next turn.
// Returns changed=false when the workspace is byte-identical to the last
// snapshot (no new undo point recorded). Best-effort by contract: callers
// ignore the error so a snapshot failure never blocks a turn.
func (t *Tracker) Track() (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ensureInit(); err != nil {
		return false, err
	}
	if err := t.add(); err != nil {
		return false, err
	}
	out, err := t.git.run("write-tree")
	if err != nil {
		return false, err
	}
	hash := strings.TrimSpace(out)
	if hash == "" {
		return false, nil
	}
	if n := len(t.stack); n > 0 && t.stack[n-1] == hash {
		return false, nil // workspace unchanged since the last snapshot
	}
	t.stack = append(t.stack, hash)
	return true, nil
}

// Undo reverts the workspace to the most recent turn-start snapshot. ok=false
// means there is nothing to undo. changed lists the restored/removed paths.
func (t *Tracker) Undo() (changed []string, ok bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.stack) == 0 {
		return nil, false, nil
	}
	hash := t.stack[len(t.stack)-1]
	t.stack = t.stack[:len(t.stack)-1]
	changed, err = t.revertTo(hash)
	if err != nil {
		return nil, true, err
	}
	return changed, true, nil
}

// Reset clears the in-memory undo stack (shadow objects stay on disk). Called
// on session switch/reset, where prior turns' snapshots no longer map to the
// new conversation.
func (t *Tracker) Reset() {
	t.mu.Lock()
	t.stack = nil
	t.mu.Unlock()
}

func (t *Tracker) ensureInit() error {
	if t.initialized {
		return nil
	}
	if _, err := os.Stat(filepath.Join(t.git.gitDir, "HEAD")); err == nil {
		t.initialized = true
		return nil
	}
	if err := os.MkdirAll(t.git.gitDir, 0o755); err != nil {
		return err
	}
	// Init via GIT_DIR/GIT_WORK_TREE env so the repo metadata lands in gitDir.
	// A `--git-dir` flag on `init` would instead create a repo *named* gitDir.
	cmd := exec.Command("git", "init", "-q")
	cmd.Env = append(noPromptEnv(), "GIT_DIR="+t.git.gitDir, "GIT_WORK_TREE="+t.git.workTree)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init shadow repo: %w: %s", err, strings.TrimSpace(string(out)))
	}
	t.initialized = true
	return nil
}

// add stages the whole workspace into the shadow index, honoring the
// workspace's own .gitignore (git reads it because work-tree points there) and
// skipping oversized untracked files via info/exclude.
func (t *Tracker) add() error {
	if err := t.excludeLargeFiles(); err != nil {
		return err
	}
	_, err := t.git.run("add", "--all")
	return err
}

// excludeLargeFiles rewrites the shadow repo's info/exclude with the set of
// untracked files over maxFileSize so the following `add --all` leaves them
// out. Rewritten each Track so the set stays current.
func (t *Tracker) excludeLargeFiles() error {
	others, err := t.git.runZ("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	var large []string
	for _, rel := range others {
		fi, statErr := os.Stat(filepath.Join(t.git.workTree, rel))
		if statErr == nil && !fi.IsDir() && fi.Size() > maxFileSize {
			large = append(large, "/"+rel)
		}
	}
	excludePath := filepath.Join(t.git.gitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return err
	}
	if len(large) == 0 {
		return os.WriteFile(excludePath, nil, 0o644)
	}
	return os.WriteFile(excludePath, []byte(strings.Join(large, "\n")+"\n"), 0o644)
}

// revertTo restores the workspace to the given tree hash: files differing from
// the hash are checked out from it; files absent in the hash (created after the
// snapshot) are deleted. Returns the affected workspace-relative paths.
func (t *Tracker) revertTo(hash string) ([]string, error) {
	// Stage current state so `diff --cached` compares hash against the live tree.
	if _, err := t.git.run("add", "--all"); err != nil {
		return nil, err
	}
	changed, err := t.git.runZ("diff", "--cached", "--name-only", "-z", hash)
	if err != nil {
		return nil, err
	}
	var done []string
	for _, rel := range changed {
		if _, err := t.git.run("checkout", hash, "--", rel); err == nil {
			done = append(done, rel)
			continue
		}
		// checkout failed. Only treat this as "created after the snapshot"
		// (and delete it) when the file genuinely isn't in the snapshot tree.
		// If ls-tree finds it, checkout failed for some other reason — keep the
		// file rather than destroy data on a transient/unexpected git error.
		if out, lsErr := t.git.run("ls-tree", hash, "--", rel); lsErr == nil && strings.TrimSpace(out) != "" {
			continue
		}
		if rmErr := os.Remove(filepath.Join(t.git.workTree, rel)); rmErr == nil || os.IsNotExist(rmErr) {
			done = append(done, rel)
		}
	}
	return done, nil
}
