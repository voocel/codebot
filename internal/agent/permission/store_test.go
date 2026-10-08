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

// List puts the newest first; Remove lasts past a reload.
func TestStoreListAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for key, at := range map[string]time.Time{"exec:make": old, "exec:touch": time.Now()} {
		if err := store.Add(StoreEntry{Key: key, AddedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.List(); len(got) != 2 || got[0].Key != "exec:touch" {
		t.Fatalf("list = %+v", got)
	}
	if err := store.Remove("exec:touch"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.List(); len(got) != 1 || got[0].Key != "exec:make" {
		t.Fatalf("after remove and reload: %+v", got)
	}
}
