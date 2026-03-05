package storage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const maxHistoryItems = 500

// HistoryEntry is one line in the JSONL history file.
type HistoryEntry struct {
	Display        string         `json:"display"`
	PastedContents map[string]any `json:"pastedContents"`
	Timestamp      int64          `json:"timestamp"`
	Project        string         `json:"project"`
	SessionID      string         `json:"sessionId,omitempty"`
}

// History provides append-only input history with per-project filtering.
// The backing file (~/.codebot/history.jsonl) is shared across all projects;
// only entries matching the current project are surfaced.
type History struct {
	path      string
	project   string
	sessionID string
	items     []string // current-project entries, newest first (index 0 = most recent)
	mu        sync.Mutex
}

// NewHistory loads history from path, filtering by project.
func NewHistory(path, project, sessionID string) *History {
	h := &History{path: path, project: project, sessionID: sessionID}
	h.load()
	return h
}

// SetSessionID updates the session ID for subsequent entries (e.g. after session switch).
func (h *History) SetSessionID(id string) {
	h.mu.Lock()
	h.sessionID = id
	h.mu.Unlock()
}

// Add appends text to history. Duplicates are moved to the front.
func (h *History) Add(text string) {
	if text == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	// Deduplicate: remove existing occurrence, then prepend.
	if idx := slices.Index(h.items, text); idx >= 0 {
		h.items = slices.Delete(h.items, idx, idx+1)
	}
	h.items = slices.Insert(h.items, 0, text)
	if len(h.items) > maxHistoryItems {
		h.items = h.items[:maxHistoryItems]
	}

	h.appendFile(text)
}

// Get returns the history entry at index (0 = most recent).
func (h *History) Get(index int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if index < 0 || index >= len(h.items) {
		return ""
	}
	return h.items[index]
}

// Len returns the number of history entries for the current project.
func (h *History) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.items)
}

// load reads the JSONL file and populates items for the current project.
func (h *History) load() {
	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer f.Close()

	seen := make(map[string]struct{})
	var all []string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var e HistoryEntry
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}
		if e.Project != h.project || e.Display == "" {
			continue
		}
		all = append(all, e.Display)
	}

	// Reverse so newest is first, then deduplicate.
	slices.Reverse(all)
	for _, text := range all {
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		h.items = append(h.items, text)
		if len(h.items) >= maxHistoryItems {
			break
		}
	}
}

// appendFile appends a single entry to the JSONL file.
func (h *History) appendFile(text string) {
	_ = os.MkdirAll(filepath.Dir(h.path), 0o755)
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	e := HistoryEntry{
		Display:        text,
		PastedContents: map[string]any{},
		Timestamp:      time.Now().UnixMilli(),
		Project:        h.project,
		SessionID:      h.sessionID,
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = f.Write(data)
}
