package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

func historyAt(t *testing.T, path string) *History {
	t.Helper()
	return NewHistory(path, "proj", "sess")
}

// Bodies have to outlive the process, or a recalled prompt sends the bare
// "[Pasted text #1]" to the model.
func TestPasteBodiesSurviveReload(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "history.jsonl")
	body := strings.Repeat("log line\n", 500)

	historyAt(t, path).Add("check [Pasted text #1 +500 lines]", map[int]string{1: body})

	reloaded := historyAt(t, path)
	if got := reloaded.Get(0); got != "check [Pasted text #1 +500 lines]" {
		t.Fatalf("text = %q", got)
	}
	if got := reloaded.Pasted(0)[1]; got != body {
		t.Fatalf("body did not survive reload (%d bytes, want %d)", len(got), len(body))
	}
}

// Storing unreferenced bodies would copy one entry's payload onto every line
// that followed it.
func TestOnlyReferencedBodiesAreStored(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "history.jsonl")
	historyAt(t, path).Add("uses #2", map[int]string{2: "kept"})

	stored := historyAt(t, path).Pasted(0)
	if len(stored) != 1 || stored[2] != "kept" {
		t.Fatalf("stored = %v", stored)
	}
}

// A line load() cannot scan stops the file there, silently dropping every
// entry after it — so an oversized entry gives up its bodies instead.
func TestOversizedPasteIsStoredWithoutBodies(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "history.jsonl")
	huge := strings.Repeat("x", MaxPastedBytes+1)

	h := historyAt(t, path)
	h.Add("huge [Pasted text #1]", map[int]string{1: huge})
	h.Add("after", nil)

	reloaded := historyAt(t, path)
	if reloaded.Len() != 2 {
		t.Fatalf("Len = %d, want both entries readable", reloaded.Len())
	}
	if got := reloaded.Get(0); got != "after" {
		t.Fatalf("newest entry = %q — the oversized line broke the load", got)
	}
	// The id survives so recall can tell this from a hand-typed reference.
	bodies := reloaded.Pasted(1)
	if body, ok := bodies[1]; !ok || body != "" {
		t.Fatalf("bodies = %v, want the id kept with no content", bodies)
	}
}

// Entries written before paste persistence carry an empty object and must
// still load.
func TestEntriesWithoutPastedContentsLoad(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "history.jsonl")
	historyAt(t, path).Add("plain text", nil)

	reloaded := historyAt(t, path)
	if got := reloaded.Get(0); got != "plain text" {
		t.Fatalf("text = %q", got)
	}
	if bodies := reloaded.Pasted(0); len(bodies) != 0 {
		t.Fatalf("bodies = %v, want none", bodies)
	}
}
