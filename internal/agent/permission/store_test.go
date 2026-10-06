package permission

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := store.Add(StoreEntry{
		Key:        "b",
		Tool:       "bash",
		Capability: CapabilityExec,
		Summary:    "echo ok",
		AddedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("Add b: %v", err)
	}
	if err := store.Add(StoreEntry{
		Key:        "a",
		Tool:       "read",
		Capability: CapabilityRead,
		Summary:    "a.txt",
		AddedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("Add a: %v", err)
	}

	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore reload: %v", err)
	}
	if !reloaded.Has("a") || !reloaded.Has("b") {
		t.Fatalf("expected persisted entries, got %#v", reloaded)
	}
}
