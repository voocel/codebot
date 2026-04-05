package storage

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/voocel/agentcore"
)

const (
	currentVersion = 3
	headerEntryID  = "h0" // well-known ID for the session header entry
)

// Store manages a single session JSONL file.
type Store struct {
	path   string
	header Header
	file   *os.File
	leafID string // ID of the most recent entry (tree tip)
	mu     sync.Mutex
}

// create creates a new session file in dir and returns the Store.
func create(dir, cwd string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	id := generateID()
	now := time.Now()
	filename := fmt.Sprintf("%s_%s.jsonl", now.Format("2006-01-02"), id)
	path := filepath.Join(dir, filename)

	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create session file: %w", err)
	}

	h := Header{
		Version:   currentVersion,
		SessionID: id,
		Cwd:       cwd,
		Created:   now,
	}

	s := &Store{path: path, header: h, file: f, leafID: headerEntryID}
	if err := s.appendEntry(Entry{
		Kind:      EntryHeader,
		ID:        headerEntryID,
		Timestamp: now,
		Data:      mustMarshal(h),
	}); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

// open opens an existing session file and reads its header.
func open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}

	// Read header from first line
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		f.Close()
		return nil, fmt.Errorf("session file is empty")
	}

	var entry Entry
	if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
		f.Close()
		return nil, fmt.Errorf("parse header: %w", err)
	}
	if entry.Kind != EntryHeader {
		f.Close()
		return nil, fmt.Errorf("first entry is %q, expected header", entry.Kind)
	}

	var h Header
	if err := json.Unmarshal(entry.Data, &h); err != nil {
		f.Close()
		return nil, fmt.Errorf("parse header data: %w", err)
	}
	if h.Version != currentVersion {
		f.Close()
		return nil, fmt.Errorf("unsupported session version: got %d, expected %d", h.Version, currentVersion)
	}

	return &Store{path: path, header: h, file: f, leafID: findLeafID(path)}, nil
}

// AppendMessage serializes and appends an agentcore.Message.
func (s *Store) AppendMessage(msg agentcore.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	return s.appendChained(EntryMessage, data)
}

// AppendModelChange records a model switch.
func (s *Store) AppendModelChange(provider, model string) error {
	data, err := json.Marshal(ModelChange{Provider: provider, Model: model})
	if err != nil {
		return err
	}
	return s.appendChained(EntryModelChange, data)
}

// AppendThinkingLevelChange records a thinking level switch.
func (s *Store) AppendThinkingLevelChange(level string) error {
	data, err := json.Marshal(ThinkingLevelChange{Level: level})
	if err != nil {
		return err
	}
	return s.appendChained(EntryThinkingChange, data)
}

// AppendCompaction records a compaction event with summary and optional kept messages.
func (s *Store) AppendCompaction(summary string, keptMessages []json.RawMessage) error {
	data, err := json.Marshal(Compaction{Summary: summary, Messages: keptMessages})
	if err != nil {
		return fmt.Errorf("marshal compaction: %w", err)
	}
	return s.appendChained(EntryCompaction, data)
}

// AppendPlanSlug records the plan file slug associated with this session.
func (s *Store) AppendPlanSlug(slug, title string) error {
	data, err := json.Marshal(PlanSlugEntry{Slug: slug, Title: title})
	if err != nil {
		return fmt.Errorf("marshal plan slug: %w", err)
	}
	return s.appendChained(EntryPlanSlug, data)
}

func (s *Store) AppendPlanState(phase, slug, title, preMode string, allowedCommands []AllowedCommandEntry) error {
	data, err := json.Marshal(PlanStateEntry{
		Phase:           phase,
		Slug:            slug,
		Title:           title,
		PreMode:         preMode,
		AllowedCommands: allowedCommands,
	})
	if err != nil {
		return fmt.Errorf("marshal plan state: %w", err)
	}
	return s.appendChained(EntryPlanState, data)
}

// SetName updates the session display name by appending a session_info entry.
func (s *Store) SetName(name string) error {
	data, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return fmt.Errorf("marshal session info: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.header.Name = name
	id := generateID()
	if err := s.appendEntry(Entry{
		Kind:      EntrySessionInfo,
		ID:        id,
		ParentID:  s.leafID,
		Timestamp: time.Now(),
		Data:      data,
	}); err != nil {
		return err
	}
	s.leafID = id
	return nil
}

// Header returns the session header.
func (s *Store) Header() Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.header
}

// Path returns the session file path.
func (s *Store) Path() string { return s.path }

// Close closes the session file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		err := s.file.Close()
		s.file = nil
		return err
	}
	return nil
}

func (s *Store) appendEntry(entry Entry) error {
	if s.file == nil {
		return fmt.Errorf("session store is closed")
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}
	line = append(line, '\n')
	_, err = s.file.Write(line)
	return err
}

// appendChained appends an entry linked to the current leaf and advances the leaf pointer.
func (s *Store) appendChained(kind EntryKind, data json.RawMessage) error {
	id := generateID()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendEntry(Entry{
		Kind:      kind,
		ID:        id,
		ParentID:  s.leafID,
		Timestamp: time.Now(),
		Data:      data,
	}); err != nil {
		return err
	}
	s.leafID = id
	return nil
}

// findLeafID scans a JSONL file and returns the ID of the last entry.
func findLeafID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return headerEntryID
	}
	defer f.Close()

	leafID := headerEntryID
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry Entry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.ID != "" {
			leafID = entry.ID
		}
	}
	return leafID
}

func generateID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xFFFFFFFF)
	}
	return hex.EncodeToString(b)
}

func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic("session: mustMarshal: " + err.Error())
	}
	return data
}
