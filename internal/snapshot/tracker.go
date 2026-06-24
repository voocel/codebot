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

// ErrTrackerClosed means a snapshot operation was attempted after Close.
var ErrTrackerClosed = errors.New("snapshot tracker closed")

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
	stack       []string // undo stack: pre-turn tree hashes, persisted to statePath
	redoStack   []string // redo stack: pre-undo workspace hashes, memory-only
	statePath   string   // sidecar persisting the undo stack across restarts; "" = memory only
	initialized bool
	gcOnce      sync.Once // guards the once-per-process background gc
	gcDone      chan struct{}
	closed      bool
}

// New returns a Tracker writing snapshots to gitDir for the given workspace.
// statePath persists the undo stack so it survives a restart/resume; pass "" to
// keep the stack in memory only. An existing statePath is loaded immediately.
func New(gitDir, workTree, statePath string) *Tracker {
	t := &Tracker{git: gitRunner{gitDir: gitDir, workTree: workTree}, statePath: statePath}
	t.load()
	return t
}

// Close waits for the tracker's background maintenance to finish.
//
// Snapshot operations are otherwise synchronous, but ensureInit starts a
// best-effort git gc in the background. Tests and runtime shutdown call Close
// so temp-dir cleanup and process teardown never race that git process.
func (t *Tracker) Close() {
	t.mu.Lock()
	t.closed = true
	done := t.gcDone
	t.mu.Unlock()
	if done != nil {
		<-done
	}
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
	hash, err := t.currentTree()
	if err != nil {
		return false, err
	}
	if hash == "" {
		return false, nil
	}
	if n := len(t.stack); n > 0 && t.stack[n-1] == hash {
		return false, nil // workspace unchanged since the last snapshot
	}
	t.stack = append(t.stack, hash)
	t.redoStack = nil // a fresh edit invalidates the redo branch
	t.persist()
	return true, nil
}

// currentTree stages the whole workspace and writes it to a tree object,
// returning the hash that captures the live workspace. Shared by Track (records
// turn-start snapshots) and Undo/Redo (snapshot the pre-revert state for the
// opposite stack). Caller holds t.mu.
func (t *Tracker) currentTree() (string, error) {
	if err := t.ensureInit(); err != nil {
		return "", err
	}
	if err := t.add(); err != nil {
		return "", err
	}
	out, err := t.git.run("write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
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
	// The snapshot may have been collected by background gc (objects past the
	// prune window are unreachable). Probe before mutating anything so a missing
	// tree reports cleanly; drop the dead point and don't touch the redo stack
	// (no workspace change happened).
	if _, probeErr := t.git.run("cat-file", "-e", hash+"^{tree}"); probeErr != nil {
		t.stack = t.stack[:len(t.stack)-1]
		t.persist()
		return nil, true, ErrSnapshotExpired
	}
	// Snapshot the current (pre-undo) workspace so Redo can return to it.
	redoHash, err := t.currentTree()
	if err != nil {
		return nil, true, err
	}
	changed, err = t.revertTo(hash)
	if err != nil {
		return nil, true, err // stacks untouched — undo can be retried
	}
	t.stack = t.stack[:len(t.stack)-1]
	t.redoStack = append(t.redoStack, redoHash)
	t.persist()
	return changed, true, nil
}

// Redo re-applies the most recently undone change, returning the workspace to
// the state captured just before that Undo. ok=false when there is nothing to
// redo (no prior Undo, or a new edit invalidated the redo branch). The redo
// stack is memory-only, so its hashes are recent and never gc-pruned mid-life.
func (t *Tracker) Redo() (changed []string, ok bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.redoStack) == 0 {
		return nil, false, nil
	}
	hash := t.redoStack[len(t.redoStack)-1]
	// Snapshot the current (pre-redo) workspace so Undo can return to it.
	undoHash, err := t.currentTree()
	if err != nil {
		return nil, true, err
	}
	changed, err = t.revertTo(hash)
	if err != nil {
		return nil, true, err // stacks untouched — redo can be retried
	}
	t.redoStack = t.redoStack[:len(t.redoStack)-1]
	t.stack = append(t.stack, undoHash)
	t.persist()
	return changed, true, nil
}

// DiffTop returns a numstat diff of the top undo snapshot against the current
// workspace — what /undo would roll back. Empty when the stack is empty. Stages
// the workspace first so untracked (newly created) files appear in the diff.
func (t *Tracker) DiffTop() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.stack) == 0 {
		return "", nil
	}
	if err := t.ensureInit(); err != nil {
		return "", err
	}
	if err := t.add(); err != nil {
		return "", err
	}
	hash := t.stack[len(t.stack)-1]
	// --no-renames (as opencode does) keeps numstat lines as "adds\tdels\tpath";
	// rename detection would emit "old => new" in the path column and break the
	// command-layer parse. A rename then shows as delete-old + add-new.
	return t.git.run("diff", "--cached", "--numstat", "--no-renames", hash)
}

// Rebind repoints the tracker at a new session's sidecar: it drops the current
// in-memory stack and loads whatever was persisted for statePath (empty when
// that session has none yet). Called on session switch/new, where the prior
// session's undo points no longer apply. Shadow objects stay on disk.
func (t *Tracker) Rebind(statePath string) {
	t.mu.Lock()
	t.statePath = statePath
	t.redoStack = nil
	t.load()
	t.mu.Unlock()
}

// RebindWorkspace repoints the tracker at a different workspace — both the
// shadow gitDir and the workTree — along with its sidecar, then reloads the
// persisted stack. Unlike Rebind (which only swaps statePath for a same-cwd
// session switch), this is for worktree enter/exit where the whole workspace
// moves. The instance is reused so callers needn't juggle Close on the old one;
// initialized is cleared so the next Track lazily inits the new shadow repo.
// The new shadow repo is not background-gc'd (gcOnce already fired) — worktree
// shadow repos are short-lived and removed with the worktree, so that's fine.
func (t *Tracker) RebindWorkspace(gitDir, workTree, statePath string) {
	t.mu.Lock()
	t.git = gitRunner{gitDir: gitDir, workTree: workTree}
	t.initialized = false
	t.statePath = statePath
	t.redoStack = nil
	t.load()
	t.mu.Unlock()
}

func (t *Tracker) ensureInit() error {
	if t.closed {
		return ErrTrackerClosed
	}
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
	t.gcOnce.Do(func() {
		done := make(chan struct{})
		t.gcDone = done
		go func() {
			defer close(done)
			t.backgroundGC()
		}()
	})
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
			large = append(large, gitignorePattern(rel))
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

// gitignorePattern turns a workspace-relative path into a literal info/exclude
// entry. The leading "/" anchors the match to the work-tree root (and
// incidentally neutralizes a leading '#' or '!', which would otherwise read as
// comment or negation). The wildmatch metacharacters \ * ? [ are backslash-
// escaped so a real name like "[id].tsx" or "a*b.bin" is matched verbatim rather
// than as a glob — without this, an oversized glob-named file slips into the
// snapshot while an unrelated file caught by the pattern is wrongly dropped.
func gitignorePattern(rel string) string {
	var b strings.Builder
	b.Grow(len(rel) + 1)
	b.WriteByte('/')
	for i := 0; i < len(rel); i++ {
		switch c := rel[i]; c {
		case '\\', '*', '?', '[':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
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
