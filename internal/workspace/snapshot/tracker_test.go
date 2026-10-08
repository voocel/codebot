package snapshot

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestTracker(t *testing.T, gitDir, workTree string) *Tracker {
	t.Helper()
	tr := New(gitDir, workTree)
	t.Cleanup(tr.Close)
	return tr
}

func newTempTracker(t *testing.T, workTree string) *Tracker {
	t.Helper()
	return newTestTracker(t, filepath.Join(t.TempDir(), "shadow"), workTree)
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func checkpoint(t *testing.T, tr *Tracker) string {
	t.Helper()
	_, tree, err := tr.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestCheckpointAndRestore(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	tr := newTempTracker(t, work)

	writeFile(t, work, "a.txt", "v1")
	first := checkpoint(t, tr)
	writeFile(t, work, "a.txt", "v2")
	writeFile(t, work, "b.txt", "new")
	second := checkpoint(t, tr)
	if again := checkpoint(t, tr); again != second {
		t.Fatalf("an unchanged workspace checkpoints as %s, then %s", second, again)
	}

	writeFile(t, work, "a.txt", "v3")
	changed, err := tr.Restore(work, second)
	if err != nil || !slices.Equal(changed, []string{"a.txt"}) {
		t.Fatalf("restore: changed=%v err=%v", changed, err)
	}
	if got := readFile(t, work, "a.txt"); got != "v2" {
		t.Fatalf("a.txt = %q, want v2", got)
	}

	// Further back, and a file created since goes.
	if _, err := tr.Restore(work, first); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, work, "a.txt"); got != "v1" {
		t.Fatalf("a.txt = %q, want v1", got)
	}
	if _, err := os.Stat(filepath.Join(work, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("b.txt should be removed, stat err = %v", err)
	}

	// And forward again: a checkpoint is no stack.
	if _, err := tr.Restore(work, second); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, work, "b.txt"); got != "new" {
		t.Fatalf("b.txt = %q, want new", got)
	}
}

// Unescaped, the pattern "[id].bin" would drop the small "i.bin" and keep the
// large "[id].bin".
func TestLargeFileGlobNameExcluded(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	tr := newTempTracker(t, work)

	if err := os.WriteFile(filepath.Join(work, "[id].bin"), make([]byte, maxFileSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, work, "i.bin", "keep me")

	out, err := tr.git.run("ls-tree", "-r", "--name-only", checkpoint(t, tr))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[id].bin") {
		t.Fatal("oversized glob-named file should be excluded")
	}
	if !strings.Contains(out, "i.bin") {
		t.Fatal("small file must not be dropped by an unescaped character class")
	}
}

// Reported paths must be relative to the work tree.
func TestNestedFile(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	tr := newTempTracker(t, work)

	writeFile(t, work, "pkg/x.go", "package pkg\n")
	tree := checkpoint(t, tr)
	writeFile(t, work, "pkg/x.go", "package pkg // edited\n")
	changed, err := tr.Restore(work, tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "pkg/x.go" {
		t.Fatalf("changed = %v, want [pkg/x.go]", changed)
	}
	if got := readFile(t, work, "pkg/x.go"); got != "package pkg\n" {
		t.Fatalf("pkg/x.go = %q, want restored", got)
	}
}

// A checkpoint belongs to its workspace's shadow repo.
func TestRebindIsolation(t *testing.T) {
	requireGit(t)
	workA, workB := t.TempDir(), t.TempDir()
	shadowA, shadowB := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")

	tr := newTestTracker(t, shadowA, workA)
	writeFile(t, workA, "f.txt", "A1")
	tree := checkpoint(t, tr)
	writeFile(t, workA, "f.txt", "A2")

	tr.Rebind(shadowB, workB)
	if _, err := tr.Restore(workA, tree); err == nil || !strings.Contains(err.Error(), "not here") {
		t.Fatalf("restoring another workspace's checkpoint: err = %v", err)
	}
	if dir, _, err := tr.Checkpoint(); err != nil || dir != workB {
		t.Fatalf("checkpointed %s, %v, want %s", dir, err, workB)
	}

	tr.Rebind(shadowA, workA)
	if _, err := tr.Restore(workA, tree); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, workA, "f.txt"); got != "A1" {
		t.Fatalf("f.txt = %q, want A1", got)
	}
}

func TestRestoreExpired(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	tr := newTempTracker(t, work)
	writeFile(t, work, "a.txt", "v1")
	checkpoint(t, tr)
	writeFile(t, work, "a.txt", "v2")

	// As if gc pruned it.
	const pruned = "0000000000000000000000000000000000000000"
	if _, err := tr.Restore(work, pruned); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
	if _, err := tr.Changed(work, pruned); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
	if got := readFile(t, work, "a.txt"); got != "v2" {
		t.Fatalf("a.txt = %q, want it untouched", got)
	}
}

func TestGCKeepsRecentCheckpoint(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	tr := newTempTracker(t, work)
	writeFile(t, work, "a.txt", "v1")
	tree := checkpoint(t, tr)
	tr.backgroundGC() // synchronous gc; recent objects must survive

	writeFile(t, work, "a.txt", "v2")
	if _, err := tr.Restore(work, tree); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, work, "a.txt"); got != "v1" {
		t.Fatalf("a.txt = %q, want v1 (gc must not prune an in-window checkpoint)", got)
	}
}

func TestChangedIncludesUntracked(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	tr := newTempTracker(t, work)

	writeFile(t, work, "a.txt", "1\n")
	tree := checkpoint(t, tr)
	writeFile(t, work, "a.txt", "1\n2\n")    // modify
	writeFile(t, work, "new.txt", "hello\n") // create (untracked)

	changed, err := tr.Changed(work, tree)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(changed, []string{"a.txt", "new.txt"}) {
		t.Fatalf("changed = %v, want the modified and the new file", changed)
	}
}
