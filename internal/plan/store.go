package plan

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

type Status string

const (
	StatusDraft     Status = "draft"
	StatusPending   Status = "pending"   // submit_plan called, awaiting approval
	StatusCompleted Status = "completed" // approved and executed
	StatusAbandoned Status = "abandoned" // /plan cancel
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type Metadata struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Status           Status `json:"status"`
	WorkingDirectory string `json:"working_directory"`
	CreatedAt        int64  `json:"created_at"` // unix ms
	UpdatedAt        int64  `json:"updated_at"`
}

type SavedPlan struct {
	Metadata Metadata `json:"metadata"`
	Content  string   `json:"content,omitempty"` // free-form plan text
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

const expiryDays = 90

// Store manages plan JSON files in a single directory.
type Store struct {
	dir string
}

// NewStore creates a Store and ensures the directory exists.
func NewStore(dir string) *Store {
	_ = os.MkdirAll(dir, 0o755)
	return &Store{dir: dir}
}

// GenerateID returns a unique plan ID: plan-{base36-timestamp}-{random-hex}.
func GenerateID() string {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 36)
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("plan-%s-%s", ts, hex.EncodeToString(b))
}

// Save writes a plan to disk atomically (write tmp + rename).
func (s *Store) Save(p *SavedPlan) error {
	p.Metadata.UpdatedAt = time.Now().UnixMilli()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}
	final := s.path(p.Metadata.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write plan tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename plan: %w", err)
	}
	return nil
}

// Load reads a single plan by ID. Returns nil, nil if not found.
func (s *Store) Load(id string) (*SavedPlan, error) {
	data, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read plan %s: %w", id, err)
	}
	var p SavedPlan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("unmarshal plan %s: %w", id, err)
	}
	return &p, nil
}

// Delete removes a plan file.
func (s *Store) Delete(id string) error {
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// List returns plans filtered by workingDirectory, sorted by updatedAt descending.
// Plans older than 90 days are skipped.
func (s *Store) List(cwd string) ([]SavedPlan, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read plans dir: %w", err)
	}

	cutoff := time.Now().AddDate(0, 0, -expiryDays).UnixMilli()
	var plans []SavedPlan
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "plan-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var p SavedPlan
		if json.Unmarshal(data, &p) != nil {
			continue
		}
		if p.Metadata.UpdatedAt < cutoff {
			continue
		}
		if cwd != "" && p.Metadata.WorkingDirectory != cwd {
			continue
		}
		plans = append(plans, p)
	}

	sort.Slice(plans, func(i, j int) bool {
		return plans[i].Metadata.UpdatedAt > plans[j].Metadata.UpdatedAt
	})
	return plans, nil
}

// UpdateStatus loads a plan, changes its status, and saves it.
func (s *Store) UpdateStatus(id string, status Status) error {
	p, err := s.Load(id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("plan %s not found", id)
	}
	p.Metadata.Status = status
	return s.Save(p)
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}
