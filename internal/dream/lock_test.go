package dream

import (
	"os"
	"strconv"
	"testing"
	"time"
)

func TestLockAcquireFresh(t *testing.T) {
	l := NewLock(t.TempDir())

	if got := l.LastConsolidatedAt(); !got.IsZero() {
		t.Fatalf("LastConsolidatedAt = %v, want zero for missing file", got)
	}

	prior, ok := l.TryAcquire()
	if !ok {
		t.Fatal("TryAcquire failed on fresh dir")
	}
	if !prior.IsZero() {
		t.Fatalf("prior = %v, want zero (no previous lock)", prior)
	}
	raw, err := os.ReadFile(l.path())
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock body = %q, want own PID", raw)
	}
}

func TestLockYieldsToLiveHolder(t *testing.T) {
	l := NewLock(t.TempDir())
	// Own PID is trivially alive and the mtime is fresh.
	if _, ok := l.TryAcquire(); !ok {
		t.Fatal("first acquire failed")
	}
	if _, ok := l.TryAcquire(); ok {
		t.Fatal("second acquire should yield to a live fresh holder")
	}
}

func TestLockReclaimsDeadPID(t *testing.T) {
	l := NewLock(t.TempDir())
	// A PID that (almost certainly) does not exist.
	if err := os.WriteFile(l.path(), []byte("999999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	prior, ok := l.TryAcquire()
	if !ok {
		t.Fatal("should reclaim a dead-PID lock")
	}
	if prior.IsZero() {
		t.Fatal("prior should carry the dead lock's mtime")
	}
}

func TestLockReclaimsStaleLiveHolder(t *testing.T) {
	l := NewLock(t.TempDir())
	if _, ok := l.TryAcquire(); !ok {
		t.Fatal("first acquire failed")
	}
	// Push the mtime past the stale horizon: even a live PID is assumed
	// to be a reused PID at that point.
	old := time.Now().Add(-2 * holderStaleAfter)
	if err := os.Chtimes(l.path(), old, old); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.TryAcquire(); !ok {
		t.Fatal("should reclaim a stale lock even with a live holder PID")
	}
}

func TestLockRollbackToPrior(t *testing.T) {
	l := NewLock(t.TempDir())
	prior := time.Now().Add(-30 * time.Hour).Truncate(time.Second)
	if err := os.WriteFile(l.path(), []byte("999999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(l.path(), prior, prior); err != nil {
		t.Fatal(err)
	}

	got, ok := l.TryAcquire()
	if !ok {
		t.Fatal("acquire failed")
	}
	if !got.Equal(prior) {
		t.Fatalf("prior = %v, want %v", got, prior)
	}

	l.Rollback(got)
	if last := l.LastConsolidatedAt(); !last.Equal(prior) {
		t.Fatalf("mtime after rollback = %v, want %v", last, prior)
	}
	raw, err := os.ReadFile(l.path())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatalf("lock body after rollback = %q, want empty", raw)
	}
}

func TestLockRollbackToZeroRemovesFile(t *testing.T) {
	l := NewLock(t.TempDir())
	if _, ok := l.TryAcquire(); !ok {
		t.Fatal("acquire failed")
	}
	l.Rollback(time.Time{})
	if _, err := os.Stat(l.path()); !os.IsNotExist(err) {
		t.Fatalf("lock file should be removed, stat err = %v", err)
	}
}

func TestLockReclaimRewritesBody(t *testing.T) {
	l := NewLock(t.TempDir())
	if err := os.WriteFile(l.path(), []byte("999999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.TryAcquire(); !ok {
		t.Fatal("acquire failed")
	}
	raw, _ := os.ReadFile(l.path())
	if string(raw) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("winner's body = %q, want own PID", raw)
	}
}
