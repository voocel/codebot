// Package snapshot checkpoints the workspace for /undo in a shadow git repo
// outside the project, with the workspace as its work tree. Snapshots are
// whole-workspace trees, so they capture changes from any source (tools,
// bash, manual edits) and never touch the user's own .git.
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

// ErrSnapshotExpired means gc pruned the snapshot. The expired entry is
// already dropped from the stack.
var ErrSnapshotExpired = errors.New("the checkpoint has expired (checkpoints are reclaimed after 7 days) and can no longer be restored")

var ErrTrackerClosed = errors.New("snapshot tracker closed")

// maxFileSize keeps large untracked files, such as build output that escaped
// .gitignore, out of snapshots.
const maxFileSize = 2 * 1024 * 1024

// gcPruneWindow bounds the shadow repo's growth. Re-adding an identical
// object doesn't refresh its mtime, so older undo points are collected even if
// still on the stack; Undo reports them as ErrSnapshotExpired.
const gcPruneWindow = "7.days"

// Tracker records one snapshot per turn: Track runs at each turn boundary and
// Undo reverts the most recent turn's file changes.
type Tracker struct {
	git         gitRunner
	mu          sync.Mutex
	stack       []string // pre-turn tree hashes, persisted to statePath
	redoStack   []string // pre-undo tree hashes, in memory only
	statePath   string
	initialized bool
	gcOnce      sync.Once
	gcDone      chan struct{}
	closed      bool
}

// New loads the undo stack from statePath, so it survives a restart.
func New(gitDir, workTree, statePath string) *Tracker {
	t := &Tracker{git: gitRunner{gitDir: gitDir, workTree: workTree}, statePath: statePath}
	t.load()
	return t
}

// Close waits for the background gc, so cleanup and shutdown don't race it.
func (t *Tracker) Close() {
	t.mu.Lock()
	t.closed = true
	done := t.gcDone
	t.mu.Unlock()
	if done != nil {
		<-done
	}
}

// load treats a missing or corrupt file as an empty stack. Caller holds t.mu,
// except New.
func (t *Tracker) load() {
	t.stack = nil
	data, err := os.ReadFile(t.statePath)
	if err != nil {
		return
	}
	var stack []string
	if json.Unmarshal(data, &stack) == nil {
		t.stack = stack
	}
}

// persist ignores errors: a lost undo stack must never break a turn. The
// fixed tmp name is safe because a session has a single writer. Caller holds
// t.mu.
func (t *Tracker) persist() {
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

// Track returns false when the workspace is unchanged since the last
// snapshot. Callers ignore its error so a snapshot failure never blocks a
// turn.
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
		return false, nil
	}
	t.stack = append(t.stack, hash)
	t.redoStack = nil
	t.persist()
	return true, nil
}

// currentTree requires t.mu.
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

// Undo returns ok=false when there is nothing to undo.
func (t *Tracker) Undo() (changed []string, ok bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.stack) == 0 {
		return nil, false, nil
	}
	hash := t.stack[len(t.stack)-1]
	// gc may have pruned the snapshot. Check before changing anything; the
	// workspace stays as is, so the redo stack does too.
	if _, probeErr := t.git.run("cat-file", "-e", hash+"^{tree}"); probeErr != nil {
		t.stack = t.stack[:len(t.stack)-1]
		t.persist()
		return nil, true, ErrSnapshotExpired
	}
	redoHash, err := t.currentTree()
	if err != nil {
		return nil, true, err
	}
	changed, err = t.revertTo(hash)
	if err != nil {
		return nil, true, err // stacks untouched, so undo can be retried
	}
	t.stack = t.stack[:len(t.stack)-1]
	t.redoStack = append(t.redoStack, redoHash)
	t.persist()
	return changed, true, nil
}

// Redo returns ok=false when there is nothing to redo. Redo snapshots live
// only in memory, so they are too recent for gc to prune.
func (t *Tracker) Redo() (changed []string, ok bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.redoStack) == 0 {
		return nil, false, nil
	}
	hash := t.redoStack[len(t.redoStack)-1]
	undoHash, err := t.currentTree()
	if err != nil {
		return nil, true, err
	}
	changed, err = t.revertTo(hash)
	if err != nil {
		return nil, true, err // stacks untouched, so redo can be retried
	}
	t.redoStack = t.redoStack[:len(t.redoStack)-1]
	t.stack = append(t.stack, undoHash)
	t.persist()
	return changed, true, nil
}

// DiffTop returns a numstat diff of what /undo would roll back.
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
	// Renames would print "old => new" as the path and break the caller's
	// parsing; without them a rename shows as a delete and an add.
	return t.git.run("diff", "--cached", "--numstat", "--no-renames", hash)
}

// Rebind switches to another workspace when the conversation enters or leaves
// a worktree. gc runs once per process, so a worktree's shadow repo is
// normally not collected; it is removed with the worktree.
func (t *Tracker) Rebind(gitDir, workTree, statePath string) {
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
		// `git --git-dir=X init` would create a repo named X, not one in X.
		cmd := exec.Command("git", "init", "-q")
		cmd.Env = append(noPromptEnv(), "GIT_DIR="+t.git.gitDir, "GIT_WORK_TREE="+t.git.workTree)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init shadow repo: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	t.initialized = true
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

// backgroundGC holds t.mu: a concurrent prune could collect the hash Undo
// just checked, and revertTo would then delete files.
func (t *Tracker) backgroundGC() {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.git.run("gc", "--prune="+gcPruneWindow, "--quiet")
}

func (t *Tracker) add() error {
	if err := t.excludeLargeFiles(); err != nil {
		return err
	}
	_, err := t.git.run("add", "--all")
	return err
}

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

// gitignorePattern matches rel literally. The leading "/" anchors it to the
// root and keeps a leading '#' or '!' from reading as a comment or negation;
// glob characters are escaped so names like "[id].tsx" match verbatim.
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

// revertTo deletes files that are not in the snapshot.
func (t *Tracker) revertTo(hash string) ([]string, error) {
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
		// Delete only files missing from the snapshot. If the snapshot has
		// it, checkout failed for another reason; keep the file.
		if out, lsErr := t.git.run("ls-tree", hash, "--", rel); lsErr == nil && strings.TrimSpace(out) != "" {
			continue
		}
		if rmErr := os.Remove(filepath.Join(t.git.workTree, rel)); rmErr == nil || os.IsNotExist(rmErr) {
			done = append(done, rel)
		}
	}
	return done, nil
}
