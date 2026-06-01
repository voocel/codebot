package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRosterStore_PersistAndReload(t *testing.T) {
	dir := t.TempDir()

	s := NewRosterStore()
	if err := s.SetDir(dir); err != nil {
		t.Fatalf("SetDir: %v", err)
	}
	s.SetTeam("alpha", "the test team")
	s.UpsertMember(RosterMember{Name: "researcher", AgentType: "general-purpose", Color: "blue", InitialPrompt: "investigate", Depth: 1, Kind: "teammate"})
	s.UpsertMember(RosterMember{Name: "tester", AgentType: "general-purpose", Depth: 1, Kind: "teammate"})

	// roster.json must exist on disk.
	if _, err := os.Stat(filepath.Join(dir, rosterFile)); err != nil {
		t.Fatalf("roster.json not written: %v", err)
	}

	// A fresh store pointed at the same dir reconstructs the roster.
	reloaded := NewRosterStore()
	if err := reloaded.SetDir(dir); err != nil {
		t.Fatalf("reload SetDir: %v", err)
	}
	got := reloaded.Snapshot()
	if got.Team != "alpha" || got.Description != "the test team" {
		t.Errorf("team = (%q, %q), want (alpha, the test team)", got.Team, got.Description)
	}
	if len(got.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(got.Members))
	}
	if got.Members[0].Name != "researcher" || got.Members[0].Color != "blue" || got.Members[0].InitialPrompt != "investigate" {
		t.Errorf("member[0] = %+v, want researcher/blue/investigate", got.Members[0])
	}
}

func TestRosterStore_UpsertReplacesByName(t *testing.T) {
	s := NewRosterStore()
	if err := s.SetDir(t.TempDir()); err != nil {
		t.Fatalf("SetDir: %v", err)
	}
	s.UpsertMember(RosterMember{Name: "researcher", AgentType: "general-purpose", Color: "blue"})
	s.UpsertMember(RosterMember{Name: "researcher", AgentType: "general-purpose", Color: "red"})

	got := s.Snapshot()
	if len(got.Members) != 1 {
		t.Fatalf("upsert by name should not duplicate; members = %d", len(got.Members))
	}
	if got.Members[0].Color != "red" {
		t.Errorf("upsert should replace; color = %q, want red", got.Members[0].Color)
	}
}

func TestRosterStore_RemoveMember(t *testing.T) {
	s := NewRosterStore()
	if err := s.SetDir(t.TempDir()); err != nil {
		t.Fatalf("SetDir: %v", err)
	}
	s.UpsertMember(RosterMember{Name: "researcher", AgentType: "general-purpose"})
	s.UpsertMember(RosterMember{Name: "tester", AgentType: "general-purpose"})
	s.RemoveMember("researcher")

	got := s.Snapshot()
	if len(got.Members) != 1 || got.Members[0].Name != "tester" {
		t.Errorf("after remove, members = %+v, want only tester", got.Members)
	}
	s.RemoveMember("nobody") // no-op, must not panic
}

func TestRosterStore_ClearRemovesFile(t *testing.T) {
	dir := t.TempDir()
	s := NewRosterStore()
	if err := s.SetDir(dir); err != nil {
		t.Fatalf("SetDir: %v", err)
	}
	s.SetTeam("alpha", "")
	s.UpsertMember(RosterMember{Name: "researcher", AgentType: "general-purpose"})

	s.Clear()

	if _, err := os.Stat(filepath.Join(dir, rosterFile)); !os.IsNotExist(err) {
		t.Errorf("Clear should remove roster.json, stat err = %v", err)
	}
	if got := s.Snapshot(); got.Team != "" || len(got.Members) != 0 {
		t.Errorf("Clear should empty the roster, got %+v", got)
	}
}

// In-memory mode (no dir) must not touch disk and must still behave correctly.
func TestRosterStore_InMemoryOnly(t *testing.T) {
	s := NewRosterStore()
	s.SetTeam("alpha", "")
	s.UpsertMember(RosterMember{Name: "researcher", AgentType: "general-purpose"})
	if got := s.Snapshot(); got.Team != "alpha" || len(got.Members) != 1 {
		t.Errorf("in-memory roster = %+v", got)
	}
}
