package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerateID(t *testing.T) {
	id1 := GenerateID()
	id2 := GenerateID()
	if id1 == id2 {
		t.Fatalf("expected unique IDs, got %s twice", id1)
	}
	if len(id1) < 15 {
		t.Fatalf("ID too short: %s", id1)
	}
	if id1[:5] != "plan-" {
		t.Fatalf("expected plan- prefix, got %s", id1)
	}
}

func TestStoreSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	now := time.Now().UnixMilli()
	p := &SavedPlan{
		Metadata: Metadata{
			ID:               "plan-test-12345678",
			Title:            "Test Plan",
			Status:           StatusDraft,
			WorkingDirectory: "/tmp/project",
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		Content: "## Step 1\nDo something\n\n## Step 2\nDo something else",
	}

	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists.
	if _, err := os.Stat(filepath.Join(dir, "plan-test-12345678.json")); err != nil {
		t.Fatalf("file not found: %v", err)
	}

	loaded, err := s.Load("plan-test-12345678")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil")
	}
	if loaded.Metadata.Title != "Test Plan" {
		t.Fatalf("title mismatch: %s", loaded.Metadata.Title)
	}
	if loaded.Content == "" {
		t.Fatal("content should not be empty")
	}

	// Load non-existent.
	missing, err := s.Load("plan-nonexistent")
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if missing != nil {
		t.Fatal("expected nil for missing plan")
	}

	// Delete.
	if err := s.Delete("plan-test-12345678"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	deleted, _ := s.Load("plan-test-12345678")
	if deleted != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestStoreListFiltersByCwd(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	now := time.Now().UnixMilli()

	for _, cwd := range []string{"/project-a", "/project-b", "/project-a"} {
		p := &SavedPlan{
			Metadata: Metadata{
				ID:               GenerateID(),
				Status:           StatusCompleted,
				WorkingDirectory: cwd,
				CreatedAt:        now,
				UpdatedAt:        now,
			},
		}
		if err := s.Save(p); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	plans, err := s.List("/project-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans for project-a, got %d", len(plans))
	}

	plans, err = s.List("/project-b")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan for project-b, got %d", len(plans))
	}

	plans, err = s.List("")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(plans) != 3 {
		t.Fatalf("expected 3 plans total, got %d", len(plans))
	}
}

func TestStoreUpdateStatus(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	now := time.Now().UnixMilli()

	p := &SavedPlan{
		Metadata: Metadata{
			ID:               "plan-update-test",
			Status:           StatusDraft,
			WorkingDirectory: "/tmp",
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		Content: "some plan content",
	}
	_ = s.Save(p)

	if err := s.UpdateStatus("plan-update-test", StatusPending); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	loaded, _ := s.Load("plan-update-test")
	if loaded.Metadata.Status != StatusPending {
		t.Fatalf("status: %s", loaded.Metadata.Status)
	}

	if err := s.UpdateStatus("plan-update-test", StatusCompleted); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	loaded, _ = s.Load("plan-update-test")
	if loaded.Metadata.Status != StatusCompleted {
		t.Fatalf("status: %s", loaded.Metadata.Status)
	}
}

func TestStoreListSkipsExpired(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	old := time.Now().AddDate(0, 0, -(expiryDays + 1)).UnixMilli()
	fresh := time.Now().UnixMilli()

	writeRawPlan(t, dir, &SavedPlan{
		Metadata: Metadata{ID: "plan-old", Status: StatusCompleted, WorkingDirectory: "/tmp", CreatedAt: old, UpdatedAt: old},
	})
	_ = s.Save(&SavedPlan{
		Metadata: Metadata{ID: "plan-fresh", Status: StatusCompleted, WorkingDirectory: "/tmp", CreatedAt: fresh, UpdatedAt: fresh},
	})

	plans, _ := s.List("/tmp")
	if len(plans) != 1 {
		t.Fatalf("expected 1 non-expired plan, got %d", len(plans))
	}
	if plans[0].Metadata.ID != "plan-fresh" {
		t.Fatalf("expected plan-fresh, got %s", plans[0].Metadata.ID)
	}
}

func writeRawPlan(t *testing.T, dir string, p *SavedPlan) {
	t.Helper()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, p.Metadata.ID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
