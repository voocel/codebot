package plan

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerateName(t *testing.T) {
	n1 := GenerateName()
	n2 := GenerateName()

	// Format: adjective-gerund-noun
	parts := countParts(n1)
	if parts != 3 {
		t.Fatalf("expected 3 parts, got %d in %q", parts, n1)
	}
	// Uniqueness (probabilistic but extremely unlikely to collide with 34*36*32 combos).
	if n1 == n2 {
		t.Fatalf("expected unique names, got %q twice", n1)
	}
}

func countParts(s string) int {
	n := 1
	for _, c := range s {
		if c == '-' {
			n++
		}
	}
	return n
}

func TestStoreSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	content := "# My Plan\n\n## Step 1\nDo something\n"
	if err := s.Save("test-plan", content); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File exists as .md
	if _, err := os.Stat(filepath.Join(dir, "test-plan.md")); err != nil {
		t.Fatalf("file not found: %v", err)
	}

	loaded, err := s.Load("test-plan")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != content {
		t.Fatalf("content mismatch: got %q", loaded)
	}

	// Load non-existent.
	missing, err := s.Load("nonexistent")
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if missing != "" {
		t.Fatal("expected empty string for missing plan")
	}

	// Delete.
	if err := s.Delete("test-plan"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	deleted, _ := s.Load("test-plan")
	if deleted != "" {
		t.Fatal("expected empty after delete")
	}
}

func TestStoreList(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	_ = s.Save("alpha-plan", "# Alpha")
	_ = s.Save("beta-plan", "# Beta")

	// Set explicit mod times to ensure deterministic ordering.
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	_ = os.Chtimes(filepath.Join(dir, "alpha-plan.md"), older, older)
	_ = os.Chtimes(filepath.Join(dir, "beta-plan.md"), newer, newer)

	// Write a non-.md file to verify it's ignored.
	_ = os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore"), 0o600)

	plans, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(plans))
	}
	// Most recent first.
	if plans[0].Name != "beta-plan" {
		t.Fatalf("expected beta-plan first, got %s", plans[0].Name)
	}
}
