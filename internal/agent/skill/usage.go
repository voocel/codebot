package skill

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const usageHalfLife = 7 * 24 * time.Hour
const usageDecayFloor = 0.10

// UsageTracker counts skill invocations in a file, so the listing can favor
// the skills used most, and lately.
type UsageTracker struct {
	path    string
	mu      sync.Mutex
	entries map[string]usageEntry
}

type usageEntry struct {
	Count      int       `json:"count"`
	LastUsedAt time.Time `json:"last_used_at"`
}

func NewUsageTracker(path string) (*UsageTracker, error) {
	t := &UsageTracker{path: path, entries: make(map[string]usageEntry)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return t, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skill usage %q: %w", path, err)
	}
	if err := json.Unmarshal(data, &t.entries); err != nil {
		return nil, fmt.Errorf("parse skill usage %q: %w", path, err)
	}
	return t, nil
}

// Record counts an invocation of the named skill at the time given.
func (t *UsageTracker) Record(name string, at time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[name]
	entry.Count++
	entry.LastUsedAt = at.UTC()
	t.entries[name] = entry
	return t.saveLocked()
}

// Scores rates each used skill at now: its count, halved for every week since
// its last use, down to a tenth.
func (t *UsageTracker) Scores(now time.Time) map[string]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	scores := make(map[string]float64, len(t.entries))
	for name, entry := range t.entries {
		age := max(now.Sub(entry.LastUsedAt), 0)
		decay := max(math.Exp2(-float64(age)/float64(usageHalfLife)), usageDecayFloor)
		scores[name] = float64(entry.Count) * decay
	}
	return scores
}

func (t *UsageTracker) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return fmt.Errorf("create skill usage dir: %w", err)
	}
	data, err := json.MarshalIndent(t.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal skill usage: %w", err)
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write skill usage temp file: %w", err)
	}
	if err := os.Rename(tmp, t.path); err != nil {
		return fmt.Errorf("replace skill usage file: %w", err)
	}
	return nil
}
