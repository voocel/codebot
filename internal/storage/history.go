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

const (
	maxHistoryItems = 500

	// MaxPastedBytes caps the paste bodies stored with one entry. Past it the
	// entry keeps its text but loses expansion — better than an unbounded
	// line, since load() stops at the first line it cannot scan and drops the
	// rest of the file with it.
	MaxPastedBytes = 1 << 20

	// maxHistoryLine must clear MaxPastedBytes with room for the surrounding
	// JSON, or entries written at the cap would be unreadable.
	maxHistoryLine = 8 << 20
)

// HistoryEntry is one line in the JSONL history file.
type HistoryEntry struct {
	Display string `json:"display"`
	// PastedContents holds the bodies behind this entry's "[Pasted text #N]"
	// references, keyed by N. Without them a recalled prompt sends the bare
	// reference as literal text.
	PastedContents map[int]string `json:"pastedContents"`
	Timestamp      int64          `json:"timestamp"`
	Project        string         `json:"project"`
	SessionID      string         `json:"sessionId,omitempty"`
}

// historyItem is one recallable entry: what to show, and what it expands to.
type historyItem struct {
	text   string
	pasted map[int]string
}

// History provides append-only input history with per-project filtering.
// The backing file (~/.codebot/history.jsonl) is shared across all projects;
// only entries matching the current project are surfaced.
type History struct {
	path      string
	project   string
	sessionID string
	items     []historyItem // current-project entries, newest first (index 0 = most recent)
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

// Add appends text along with the paste bodies it references (nil when it has
// none). Duplicates are moved to the front.
func (h *History) Add(text string, pasted map[int]string) {
	if text == "" {
		return
	}
	pasted = withinPasteBudget(pasted)

	h.mu.Lock()
	defer h.mu.Unlock()

	// Deduplicate: remove existing occurrence, then prepend.
	if idx := slices.IndexFunc(h.items, func(it historyItem) bool { return it.text == text }); idx >= 0 {
		h.items = slices.Delete(h.items, idx, idx+1)
	}
	h.items = slices.Insert(h.items, 0, historyItem{text: text, pasted: pasted})
	if len(h.items) > maxHistoryItems {
		h.items = h.items[:maxHistoryItems]
	}

	h.appendFile(text, pasted)
}

// Get returns the history entry at index (0 = most recent).
func (h *History) Get(index int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if index < 0 || index >= len(h.items) {
		return ""
	}
	return h.items[index].text
}

// Pasted returns the entry's paste bodies keyed by reference id, nil if none.
func (h *History) Pasted(index int) map[int]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if index < 0 || index >= len(h.items) {
		return nil
	}
	return h.items[index].pasted
}

// withinPasteBudget empties the bodies of an entry too large to read back.
// The ids stay: they are what tells recall "we dropped this" apart from "the
// user typed [Pasted text #7] by hand", which must not be rewritten.
func withinPasteBudget(pasted map[int]string) map[int]string {
	total := 0
	for _, body := range pasted {
		total += len(body)
	}
	if total <= MaxPastedBytes {
		return pasted
	}
	dropped := make(map[int]string, len(pasted))
	for id := range pasted {
		dropped[id] = ""
	}
	return dropped
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
	var all []historyItem

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxHistoryLine)
	for scanner.Scan() {
		var e HistoryEntry
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}
		if e.Project != h.project || e.Display == "" {
			continue
		}
		all = append(all, historyItem{text: e.Display, pasted: e.PastedContents})
	}

	// Reverse so newest is first, then deduplicate.
	slices.Reverse(all)
	for _, item := range all {
		if _, ok := seen[item.text]; ok {
			continue
		}
		seen[item.text] = struct{}{}
		h.items = append(h.items, item)
		if len(h.items) >= maxHistoryItems {
			break
		}
	}
}

// appendFile appends a single entry to the JSONL file.
func (h *History) appendFile(text string, pasted map[int]string) {
	_ = os.MkdirAll(filepath.Dir(h.path), 0o755)
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	if pasted == nil {
		pasted = map[int]string{}
	}
	e := HistoryEntry{
		Display:        text,
		PastedContents: pasted,
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
