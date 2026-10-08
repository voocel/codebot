// Package snapshot checkpoints the workspace for /rewind in a shadow git
// repo outside the project, with the workspace as its work tree.
// Checkpoints are whole-workspace trees, so they capture changes from any
// source (tools, bash, manual edits) and never touch the user's own .git.
package snapshot

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ErrExpired means gc pruned the checkpoint.
var ErrExpired = errors.New("the checkpoint has expired (checkpoints are reclaimed after 7 days) and can no longer be restored")

var ErrTrackerClosed = errors.New("snapshot tracker closed")

// maxFileSize keeps large untracked files, such as build output that escaped
// .gitignore, out of checkpoints.
const maxFileSize = 2 * 1024 * 1024

// gcPruneWindow bounds the shadow repo's growth. No ref holds a checkpoint,
// and re-adding an identical object doesn't refresh its mtime, so gc
// collects checkpoints older than this however recently they were taken
// again.
const gcPruneWindow = "7.days"

// Tracker checkpoints a workspace and restores it to a checkpoint; the
// conversation keeps the checkpoints.
type Tracker struct {
	git         gitRunner
	mu          sync.Mutex
	initialized bool
	gcOnce      sync.Once
	gcDone      chan struct{}
	closed      bool
}

func New(gitDir, workTree string) *Tracker {
	return &Tracker{git: gitRunner{gitDir: gitDir, workTree: workTree}}
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

// Checkpoint records the workspace as it is and returns it with its tree.
func (t *Tracker) Checkpoint() (dir, tree string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tree, err = t.currentTree()
	return t.git.workTree, tree, err
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

// Changed lists the files of dir that differ from tree: those Restore would
// put back.
func (t *Tracker) Changed(dir, tree string) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.reach(dir, tree); err != nil {
		return nil, err
	}
	if err := t.add(); err != nil {
		return nil, err
	}
	// Without renames, a renamed file shows as its two paths, each of which
	// Restore puts back.
	return t.git.runZ("diff", "--cached", "--name-only", "--no-renames", "-z", tree)
}

// Restore returns dir to tree and reports the files it changed.
func (t *Tracker) Restore(dir, tree string) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Check before changing anything.
	if err := t.reach(dir, tree); err != nil {
		return nil, err
	}
	return t.revertTo(tree)
}

// reach checks that tree is of dir, the workspace now, and still in its
// shadow repo. Caller holds t.mu.
func (t *Tracker) reach(dir, tree string) error {
	if dir != t.git.workTree {
		return fmt.Errorf("the files were checkpointed in %s, not here", dir)
	}
	if err := t.ensureInit(); err != nil {
		return err
	}
	if _, err := t.git.run("cat-file", "-e", tree+"^{tree}"); err != nil {
		return ErrExpired
	}
	return nil
}

// Rebind switches to another workspace when the conversation enters or leaves
// a worktree. gc runs once per process, so a worktree's shadow repo is
// normally not collected; it is removed with the worktree.
func (t *Tracker) Rebind(gitDir, workTree string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.git = gitRunner{gitDir: gitDir, workTree: workTree}
	t.initialized = false
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

// backgroundGC holds t.mu: a concurrent prune could collect the tree Restore
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

// revertTo deletes files that are not in the checkpoint. Caller holds t.mu.
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
		// Delete only files missing from the checkpoint. If it has the file,
		// checkout failed for another reason; keep the file.
		if out, lsErr := t.git.run("ls-tree", hash, "--", rel); lsErr == nil && strings.TrimSpace(out) != "" {
			continue
		}
		if rmErr := os.Remove(filepath.Join(t.git.workTree, rel)); rmErr == nil || os.IsNotExist(rmErr) {
			done = append(done, rel)
		}
	}
	return done, nil
}
