package snapshot

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestTrackAndUndo exercises the full round-trip: modify-then-undo restores
// prior content, and undoing a turn that created a file removes that file.
func TestTrackAndUndo(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	tr := New(filepath.Join(t.TempDir(), "shadow"), work, "")

	writeFile(t, work, "a.txt", "v1")
	if _, err := tr.Track(); err != nil { // snapshot 1: a=v1
		t.Fatal(err)
	}

	writeFile(t, work, "a.txt", "v2")
	writeFile(t, work, "b.txt", "new")
	if _, err := tr.Track(); err != nil { // snapshot 2: a=v2, b=new
		t.Fatal(err)
	}

	// Current turn edits a again; undo should restore snapshot 2.
	writeFile(t, work, "a.txt", "v3")
	changed, ok, err := tr.Undo()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(changed) == 0 {
		t.Fatal("expected changed files")
	}
	if got := readFile(t, work, "a.txt"); got != "v2" {
		t.Fatalf("a.txt = %q, want v2", got)
	}

	// Undo again: restore snapshot 1 (a=v1) and delete the later-created b.txt.
	if _, ok, err = tr.Undo(); err != nil || !ok {
		t.Fatalf("second undo: ok=%v err=%v", ok, err)
	}
	if got := readFile(t, work, "a.txt"); got != "v1" {
		t.Fatalf("a.txt = %q, want v1", got)
	}
	if _, err := os.Stat(filepath.Join(work, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("b.txt should be removed, stat err = %v", err)
	}

	// Empty stack: nothing to undo.
	if _, ok, _ := tr.Undo(); ok {
		t.Fatal("expected ok=false on empty stack")
	}
}

// TestUnchangedTurnSkipped verifies a turn that changes nothing does not push a
// duplicate snapshot (identical tree hash).
func TestUnchangedTurnSkipped(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	tr := New(filepath.Join(t.TempDir(), "shadow"), work, "")

	writeFile(t, work, "a.txt", "v1")
	if changed, _ := tr.Track(); !changed {
		t.Fatal("first track should record a snapshot")
	}
	if changed, _ := tr.Track(); changed {
		t.Fatal("second track with no edits should be skipped")
	}
}

// TestLargeFileExcluded verifies oversized files stay out of snapshots.
func TestLargeFileExcluded(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	tr := New(filepath.Join(t.TempDir(), "shadow"), work, "")

	writeFile(t, work, "small.txt", "ok")
	if err := os.WriteFile(filepath.Join(work, "big.bin"), make([]byte, maxFileSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Track(); err != nil {
		t.Fatal(err)
	}

	hash := tr.stack[len(tr.stack)-1]
	out, err := tr.git.run("ls-tree", "-r", "--name-only", hash)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "big.bin") {
		t.Fatal("oversized file should be excluded from the snapshot")
	}
	if !strings.Contains(out, "small.txt") {
		t.Fatal("small file should be in the snapshot")
	}
}

// TestNestedFile verifies snapshot/revert works for files in subdirectories
// and that reported paths are work-tree-relative (guards the cmd.Dir anchoring).
func TestNestedFile(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	tr := New(filepath.Join(t.TempDir(), "shadow"), work, "")

	writeFile(t, work, "pkg/x.go", "package pkg\n")
	if _, err := tr.Track(); err != nil {
		t.Fatal(err)
	}

	writeFile(t, work, "pkg/x.go", "package pkg // edited\n")
	changed, ok, err := tr.Undo()
	if err != nil || !ok {
		t.Fatalf("undo: ok=%v err=%v", ok, err)
	}
	if len(changed) != 1 || changed[0] != "pkg/x.go" {
		t.Fatalf("changed = %v, want [pkg/x.go]", changed)
	}
	if got := readFile(t, work, "pkg/x.go"); got != "package pkg\n" {
		t.Fatalf("pkg/x.go = %q, want restored", got)
	}
}

// TestPersistAndReload verifies the undo stack survives reconstructing the
// Tracker over the same shadow repo + sidecar (the resume scenario).
func TestPersistAndReload(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	shadow := filepath.Join(t.TempDir(), "shadow")
	state := filepath.Join(t.TempDir(), "undo-stack.json")

	tr := New(shadow, work, state)
	writeFile(t, work, "a.txt", "v1")
	if _, err := tr.Track(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, work, "a.txt", "v2")
	if _, err := tr.Track(); err != nil {
		t.Fatal(err)
	}

	// Fresh Tracker over the same shadow + sidecar: stack reloads from disk.
	tr2 := New(shadow, work, state)
	writeFile(t, work, "a.txt", "v3")
	if _, ok, err := tr2.Undo(); err != nil || !ok {
		t.Fatalf("undo after reload: ok=%v err=%v", ok, err)
	}
	if got := readFile(t, work, "a.txt"); got != "v2" {
		t.Fatalf("a.txt = %q, want v2 (restored from reloaded stack)", got)
	}
}

// TestRebindIsolation verifies Rebind swaps to another session's sidecar:
// the old stack is dropped and the target sidecar's stack loaded.
func TestRebindIsolation(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	shadow := filepath.Join(t.TempDir(), "shadow")
	stateA := filepath.Join(t.TempDir(), "a.json")
	stateB := filepath.Join(t.TempDir(), "b.json")

	tr := New(shadow, work, stateA)
	writeFile(t, work, "f.txt", "A1")
	if _, err := tr.Track(); err != nil {
		t.Fatal(err)
	}

	// Rebind to a fresh session's (nonexistent) sidecar → empty stack.
	tr.Rebind(stateB)
	if _, ok, _ := tr.Undo(); ok {
		t.Fatal("after Rebind to empty sidecar, undo should report nothing")
	}

	// Rebind back to A → its persisted stack reloads.
	tr.Rebind(stateA)
	writeFile(t, work, "f.txt", "A2")
	if _, ok, err := tr.Undo(); err != nil || !ok {
		t.Fatalf("undo after rebind back: ok=%v err=%v", ok, err)
	}
	if got := readFile(t, work, "f.txt"); got != "A1" {
		t.Fatalf("f.txt = %q, want A1", got)
	}
}

// TestUndoExpiredSnapshot verifies a stack hash whose tree object is gone
// (simulating a gc-pruned snapshot) degrades to ErrSnapshotExpired, pops the
// dead hash, and lets the next undo reach a live snapshot.
func TestUndoExpiredSnapshot(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	tr := New(filepath.Join(t.TempDir(), "shadow"), work, "")
	writeFile(t, work, "a.txt", "v1")
	if _, err := tr.Track(); err != nil { // real snapshot + inits shadow repo
		t.Fatal(err)
	}
	// Push a hash absent from the shadow repo (as if gc pruned it).
	tr.stack = append(tr.stack, "0000000000000000000000000000000000000000")

	if _, ok, err := tr.Undo(); !ok || !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("expired snapshot: ok=%v err=%v, want ok=true + ErrSnapshotExpired", ok, err)
	}
	// Dead hash popped; the real snapshot underneath still restores.
	writeFile(t, work, "a.txt", "v2")
	if _, ok, err := tr.Undo(); !ok || err != nil {
		t.Fatalf("second undo: ok=%v err=%v", ok, err)
	}
	if got := readFile(t, work, "a.txt"); got != "v1" {
		t.Fatalf("a.txt = %q, want v1", got)
	}
}

// TestGCKeepsRecentSnapshot verifies background gc does not prune in-window
// snapshots — undo still works right after a collection.
func TestGCKeepsRecentSnapshot(t *testing.T) {
	requireGit(t)
	work := t.TempDir()
	tr := New(filepath.Join(t.TempDir(), "shadow"), work, "")
	writeFile(t, work, "a.txt", "v1")
	if _, err := tr.Track(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, work, "a.txt", "v2")
	if _, err := tr.Track(); err != nil {
		t.Fatal(err)
	}
	tr.backgroundGC() // synchronous gc; recent objects must survive

	writeFile(t, work, "a.txt", "v3")
	if _, ok, err := tr.Undo(); err != nil || !ok {
		t.Fatalf("undo after gc: ok=%v err=%v", ok, err)
	}
	if got := readFile(t, work, "a.txt"); got != "v2" {
		t.Fatalf("a.txt = %q, want v2 (gc must not prune in-window snapshot)", got)
	}
}
