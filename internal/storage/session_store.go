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

	// openEntries caches the full entries map produced by the open-time
	// scan so the first BuildSnapshot can reuse it instead of re-reading
	// the whole file. Consumed (set to nil) on first BuildSnapshot read,
	// and invalidated by any appendEntry — both paths ensure reads after
	// a write fall back to a fresh scan, so cache staleness can't surface.
	// create() leaves it nil; the new-session path's BuildSnapshot is
	// cheap (header-only file) and not on a hot path.
	openEntries map[string]Entry
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
//
// Performs a single full-file scan up front to recover both the leaf ID and
// the entries map; the latter is cached on the Store so the first
// BuildSnapshot doesn't re-open and re-scan the same bytes (the prior
// implementation paid for that scan twice — findLeafID at open + a fresh
// read in BuildSnapshot).
func open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}

	entries, leafID, err := scanEntries(path)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("scan session: %w", err)
	}

	headerEntry, ok := entries[headerEntryID]
	if !ok {
		f.Close()
		return nil, fmt.Errorf("session file missing header entry")
	}
	if headerEntry.Kind != EntryHeader {
		f.Close()
		return nil, fmt.Errorf("first entry is %q, expected header", headerEntry.Kind)
	}

	var h Header
	if err := json.Unmarshal(headerEntry.Data, &h); err != nil {
		f.Close()
		return nil, fmt.Errorf("parse header data: %w", err)
	}
	if h.Version != currentVersion {
		f.Close()
		return nil, fmt.Errorf("unsupported session version: got %d, expected %d", h.Version, currentVersion)
	}

	return &Store{path: path, header: h, file: f, leafID: leafID, openEntries: entries}, nil
}

// AppendMessage serializes and appends an agentcore.Message.
//
// Thinking blocks are truncated to maxStoredThinkingRunes before write to
// keep session files compact. Truncation is storage-only — the in-memory
// message retains full thinking for this turn, since agentcore may still
// need it during the same agent_end lifecycle.
func (s *Store) AppendMessage(msg agentcore.Message) error {
	stored := trimThinkingForStorage(msg)
	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	return s.appendChained(EntryMessage, data)
}

const maxStoredThinkingRunes = 200

// trimThinkingForStorage returns a copy of msg with each thinking block
// truncated to maxStoredThinkingRunes. Other content blocks are unchanged.
// When no trimming is needed the input is returned as-is (no allocation).
func trimThinkingForStorage(msg agentcore.Message) agentcore.Message {
	needClone := false
	for _, b := range msg.Content {
		if b.Type == agentcore.ContentThinking && utf8RuneCount(b.Thinking) > maxStoredThinkingRunes {
			needClone = true
			break
		}
	}
	if !needClone {
		return msg
	}
	cloned := make([]agentcore.ContentBlock, len(msg.Content))
	copy(cloned, msg.Content)
	for i := range cloned {
		if cloned[i].Type != agentcore.ContentThinking {
			continue
		}
		runes := []rune(cloned[i].Thinking)
		if len(runes) > maxStoredThinkingRunes {
			cloned[i].Thinking = string(runes[:maxStoredThinkingRunes-1]) + "…"
		}
	}
	msg.Content = cloned
	return msg
}

func utf8RuneCount(s string) int { return len([]rune(s)) }

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

// AppendLLMCall records a single LLM response's observability metadata.
// This is written alongside the assistant message itself, so the message
// payload stays minimal while diagnostics (cache/latency/provider) stay
// queryable without replaying the whole message.
func (s *Store) AppendLLMCall(entry LLMCallEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal llm_call: %w", err)
	}
	return s.appendChained(EntryLLMCall, data)
}

func (s *Store) AppendPlanState(phase, slug, preMode string) error {
	data, err := json.Marshal(PlanStateEntry{
		Phase:   phase,
		Slug:    slug,
		PreMode: preMode,
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
	// Any append invalidates the open-time entries cache. Race-safety:
	// post-construction callers (appendChained, SetName) both hold s.mu;
	// create() doesn't hold the lock but the Store hasn't been returned to
	// any caller yet, so the assignment is still safe. A future caller
	// added to a path that doesn't satisfy either invariant must take
	// s.mu before calling appendEntry.
	s.openEntries = nil
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

// scanEntries reads a JSONL session file once and returns all entries keyed
// by ID along with the leaf ID (last entry with a non-empty ID). Replaces
// the prior findLeafID + per-BuildSnapshot scan pair: callers that need
// either the leaf or the full map can share a single IO pass.
//
// Tolerance policy mirrors the prior findLeafID + BuildSnapshot pair so
// any session file the old code could open still opens here. The only
// hard error is "file cannot be opened at all":
//
//   - Lines that fail json.Unmarshal or carry an empty ID are skipped.
//   - Duplicate IDs overwrite (last-write-wins) instead of failing —
//     mirrors old findLeafID. The chain walk in BuildSnapshot will still
//     surface real corruption (missing parent, cycle), but a stray dup
//     mid-file shouldn't lock a user out of an otherwise-recoverable
//     session.
//   - scanner.Err() is ignored. The most common trigger is a single
//     entry serialised larger than the 1MB scanner buffer (huge tool
//     results from read/grep/bash). Returning an error there would make
//     resume fail entirely; instead we return the entries scanned so far
//     and let downstream code work with what's recoverable.
func scanEntries(path string) (map[string]Entry, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, headerEntryID, err
	}
	defer f.Close()

	entries := make(map[string]Entry)
	leafID := headerEntryID
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry Entry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.ID == "" {
			continue
		}
		entries[entry.ID] = entry
		leafID = entry.ID
	}
	return entries, leafID, nil
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
