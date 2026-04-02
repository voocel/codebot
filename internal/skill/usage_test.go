package skill

import (
	"path/filepath"
	"testing"
	"time"
)

func TestUsageTrackerPersistsAndDecaysScores(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "skill-usage.json")
	base := time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)

	tracker, err := NewUsageTracker(path)
	if err != nil {
		t.Fatalf("NewUsageTracker: %v", err)
	}
	tracker.now = func() time.Time { return base }

	if err := tracker.Record("Review", base); err != nil {
		t.Fatalf("Record review: %v", err)
	}
	if err := tracker.Record("review", base.Add(2*time.Hour)); err != nil {
		t.Fatalf("Record review again: %v", err)
	}
	if err := tracker.Record("debug", base.Add(24*time.Hour)); err != nil {
		t.Fatalf("Record debug: %v", err)
	}

	reloaded, err := NewUsageTracker(path)
	if err != nil {
		t.Fatalf("Reload tracker: %v", err)
	}

	nowScores := reloaded.Scores(base.Add(24 * time.Hour))
	if nowScores["review"] <= nowScores["debug"] {
		t.Fatalf("expected review to outrank debug, got %#v", nowScores)
	}

	agedScores := reloaded.Scores(base.Add(21 * 24 * time.Hour))
	if agedScores["review"] >= nowScores["review"] {
		t.Fatalf("expected aged review score to decay, now=%v aged=%v", nowScores["review"], agedScores["review"])
	}
	if agedScores["review"] <= 0 {
		t.Fatalf("expected decay floor to keep score positive, got %#v", agedScores)
	}
}
