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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ErrSnapshotExpired means the popped snapshot's tree object is gone — the
// background gc pruned it (objects older than the prune window are unreachable
// and get collected). The undo point is unrecoverable; callers report it and
// move on. The expired hash is already dropped from the stack.
var ErrSnapshotExpired = errors.New("snapshot expired or unavailable")

// maxFileSize caps which untracked files enter a snapshot. Larger files are
// excluded so the shadow repo doesn't balloon on build artifacts or binaries
// that escaped .gitignore. Mirrors opencode's 2 MiB limit.
const maxFileSize = 2 * 1024 * 1024

// gcPruneWindow bounds shadow-repo growth: snapshot objects untouched for this
// long become reclaimable. Matches opencode's 7-day cleanup window. Note that
// git's content addressing means a re-added identical object does NOT refresh
// its mtime, so undo points older than this window do get collected — Undo's
// ErrSnapshotExpired path handles that.
const gcPruneWindow = "7.days"

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
	statePath   string // sidecar file persisting stack across restarts; "" = memory only
	initialized bool
	gcOnce      sync.Once // guards the once-per-process background gc
}

// New returns a Tracker writing snapshots to gitDir for the given workspace.
// statePath persists the undo stack so it survives a restart/resume; pass "" to
// keep the stack in memory only. An existing statePath is loaded immediately.
func New(gitDir, workTree, statePath string) *Tracker {
	t := &Tracker{git: gitRunner{gitDir: gitDir, workTree: workTree}, statePath: statePath}
	t.load()
	return t
}

// load replaces the in-memory stack with the persisted one. Best-effort: a
// missing or corrupt sidecar yields an empty stack (New has no error return).
// Caller holds t.mu, except the New/Rebind paths which own the Tracker.
func (t *Tracker) load() {
	t.stack = nil
	if t.statePath == "" {
		return
	}
	data, err := os.ReadFile(t.statePath)
	if err != nil {
		return
	}
	var stack []string
	if json.Unmarshal(data, &stack) == nil {
		t.stack = stack
	}
}

// persist writes the stack to statePath via tmp+rename so a crash mid-write
// can't corrupt the sidecar. A fixed tmp name suffices — a session has a single
// writer. Best-effort: every error is swallowed (Track/Undo callers ignore
// snapshot failures; a lost stack must never break a turn). Caller holds t.mu.
func (t *Tracker) persist() {
	if t.statePath == "" {
		return
	}
	data, err := json.Marshal(t.stack)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(t.statePath), 0o755); err != nil {
		return
	}
	tmp := t.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, t.statePath)
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
	t.persist()
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
	t.persist()
	// The snapshot may have been collected by background gc (objects past the
	// prune window are unreachable). Probe before reverting so a missing tree
	// reports cleanly instead of failing deep inside revertTo.
	if _, probeErr := t.git.run("cat-file", "-e", hash+"^{tree}"); probeErr != nil {
		return nil, true, ErrSnapshotExpired
	}
	changed, err = t.revertTo(hash)
	if err != nil {
		return nil, true, err
	}
	return changed, true, nil
}

// Rebind repoints the tracker at a new session's sidecar: it drops the current
// in-memory stack and loads whatever was persisted for statePath (empty when
// that session has none yet). Called on session switch/new, where the prior
// session's undo points no longer apply. Shadow objects stay on disk.
func (t *Tracker) Rebind(statePath string) {
	t.mu.Lock()
	t.statePath = statePath
	t.load()
	t.mu.Unlock()
}

func (t *Tracker) ensureInit() error {
	if t.initialized {
		return nil
	}
	if _, err := os.Stat(filepath.Join(t.git.gitDir, "HEAD")); err != nil {
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
	}
	t.initialized = true
	// Reclaim the shadow repo once per process, now that it's known to exist.
	// sync.Once collapses repeated ensureInit calls (every Track) to a single gc.
	t.gcOnce.Do(func() { go t.backgroundGC() })
	return nil
}

// backgroundGC reclaims the shadow repo so it doesn't grow unbounded. Snapshots
// are unreachable (we only write-tree, never commit), so gc prunes tree/blob
// objects past gcPruneWindow. Best-effort: a failure just leaves the repo
// larger. Holds t.mu so gc is mutually exclusive with Track/Undo (mirrors
// opencode's locked cleanup) — otherwise a concurrent prune could collect the
// very hash Undo just probed, dropping it into revertTo's delete path.
func (t *Tracker) backgroundGC() {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.git.run("gc", "--prune="+gcPruneWindow, "--quiet")
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
