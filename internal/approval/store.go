package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type storedEntry struct {
	Key        string     `json:"key"`
	Tool       string     `json:"tool"`
	Capability Capability `json:"capability"`
	Summary    string     `json:"summary"`
	AddedAt    time.Time  `json:"added_at"`
}

type Store struct {
	mu      sync.RWMutex
	path    string
	entries map[string]storedEntry
}

func NewStore(path string) (*Store, error) {
	s := &Store{
		path:    path,
		entries: make(map[string]storedEntry),
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var rows []storedEntry
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Key != "" {
			s.entries[row.Key] = row
		}
	}
	return s, nil
}

func (s *Store) Has(key string) bool {
	if s == nil || key == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.entries[key]
	return ok
}

func (s *Store) Add(entry storedEntry) error {
	if s == nil || entry.Key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[entry.Key] = entry
	return s.save()
}

func (s *Store) save() error {
	// 调用方必须先持有 s.mu。
	rows := make([]storedEntry, 0, len(s.entries))
	for _, row := range s.entries {
		rows = append(rows, row)
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(s)))
	return hex.EncodeToString(sum[:8])
}
