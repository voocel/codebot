package dream

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func touch(t *testing.T, dir, name string, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestCountSessionsTouchedSince(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	since := now.Add(-24 * time.Hour)

	touch(t, dir, "2026-07-13_aaaa1111.jsonl", now) // counted
	touch(t, dir, "2026-07-14_bbbb2222.jsonl", now) // counted
	touch(t, dir, "2026-07-01_cccc3333.jsonl", old) // too old
	touch(t, dir, "2026-07-14_dddd4444.jsonl", now) // current session, excluded
	touch(t, dir, "session-memory.md", now)         // not a transcript
	if err := os.Mkdir(filepath.Join(dir, "sub.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := countSessionsTouchedSince(dir, since, "dddd4444"); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	if got := countSessionsTouchedSince(dir, since, ""); got != 3 {
		t.Fatalf("count without exclusion = %d, want 3", got)
	}
}

func TestCountSessionsMissingDir(t *testing.T) {
	if got := countSessionsTouchedSince(filepath.Join(t.TempDir(), "nope"), time.Time{}, ""); got != 0 {
		t.Fatalf("count = %d, want 0 for missing dir", got)
	}
}
