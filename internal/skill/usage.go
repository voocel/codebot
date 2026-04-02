package skill

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const usageHalfLife = 7 * 24 * time.Hour
const usageDecayFloor = 0.10

type UsageTracker struct {
	path    string
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]usageEntry
}

type usageEntry struct {
	Count      int       `json:"count"`
	LastUsedAt time.Time `json:"last_used_at"`
}

func NewUsageTracker(path string) (*UsageTracker, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("skill usage path is empty")
	}

	tracker := &UsageTracker{
		path:    path,
		now:     time.Now,
		entries: make(map[string]usageEntry),
	}
	if err := tracker.load(); err != nil {
		return nil, err
	}
	return tracker, nil
}

func (t *UsageTracker) Record(name string, usedAt time.Time) error {
	if t == nil {
		return nil
	}

	name = NormalizeName(name)
	if name == "" {
		return nil
	}
	if usedAt.IsZero() {
		usedAt = t.now()
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	entry := t.entries[name]
	entry.Count++
	entry.LastUsedAt = usedAt.UTC()
	t.entries[name] = entry
	return t.saveLocked()
}

func (t *UsageTracker) Scores(now time.Time) map[string]float64 {
	if t == nil {
		return nil
	}
	if now.IsZero() {
		now = t.now()
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.entries) == 0 {
		return nil
	}
	scores := make(map[string]float64, len(t.entries))
	for name, entry := range t.entries {
		score := usageScore(entry, now)
		if score > 0 {
			scores[name] = score
		}
	}
	if len(scores) == 0 {
		return nil
	}
	return scores
}

func usageScore(entry usageEntry, now time.Time) float64 {
	if entry.Count <= 0 {
		return 0
	}
	if entry.LastUsedAt.IsZero() {
		return float64(entry.Count)
	}

	age := now.Sub(entry.LastUsedAt)
	if age < 0 {
		age = 0
	}
	decay := math.Exp2(-float64(age) / float64(usageHalfLife))
	if decay < usageDecayFloor {
		decay = usageDecayFloor
	}
	return float64(entry.Count) * decay
}

func (t *UsageTracker) load() error {
	data, err := os.ReadFile(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read skill usage %q: %w", t.path, err)
	}
	var entries map[string]usageEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parse skill usage %q: %w", t.path, err)
	}
	if len(entries) == 0 {
		return nil
	}
	normalizedEntries := make(map[string]usageEntry, len(entries))
	for name, entry := range entries {
		normalized := NormalizeName(name)
		if normalized == "" || entry.Count <= 0 {
			continue
		}
		existing := normalizedEntries[normalized]
		existing.Count += entry.Count
		if entry.LastUsedAt.After(existing.LastUsedAt) {
			existing.LastUsedAt = entry.LastUsedAt
		}
		normalizedEntries[normalized] = existing
	}
	t.entries = normalizedEntries
	return nil
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
