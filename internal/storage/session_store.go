package storage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/voocel/agentcore"
)

const currentVersion = 5

// Store appends to a single session JSONL file. Appends are serialized, so a
// Store may be shared between goroutines.
type Store struct {
	path   string
	header Header
	mu     sync.Mutex
	file   *os.File
}

// create creates a new session file in dir.
func create(dir, cwd string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	id := generateID()
	now := time.Now()
	path := filepath.Join(dir, fmt.Sprintf("%s_%s.jsonl", now.Format("2006-01-02"), id))
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create session file: %w", err)
	}
	s := &Store{path: path, file: f, header: Header{
		Version:   currentVersion,
		SessionID: id,
		Cwd:       cwd,
		Created:   now,
	}}
	if err := s.append(entryHeader, s.header); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

// open opens an existing session file for appending and replays it. A torn
// final line left by a crash is cut off first.
func open(path string) (*Store, State, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return nil, State{}, fmt.Errorf("open session: %w", err)
	}
	h, state, scan, err := replay(f)
	if err == nil {
		err = normalizeJSONLTail(f, scan)
	}
	if err != nil {
		f.Close()
		return nil, State{}, fmt.Errorf("open session %s: %w", filepath.Base(path), err)
	}
	return &Store{path: path, header: h, file: f}, state, nil
}

// Replay reads the state of the session file at path.
func Replay(path string) (State, error) {
	f, err := os.Open(path)
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	_, state, _, err := replay(f)
	return state, err
}

func replay(r io.Reader) (Header, State, jsonlScanResult, error) {
	var h Header
	var state State
	first := true
	scan, err := scanJSONLines(r, func(line []byte) error {
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			return err
		}
		if first != (e.Kind == entryHeader) {
			return fmt.Errorf("unexpected %q entry", e.Kind)
		}
		first = false
		return state.apply(e, &h)
	})
	if err == nil && first {
		err = errors.New("missing header")
	}
	return h, state, scan, err
}

func (s *State) apply(e entry, h *Header) error {
	switch e.Kind {
	case entryHeader:
		if err := json.Unmarshal(e.Data, h); err != nil {
			return err
		}
		if h.Version != currentVersion {
			return fmt.Errorf("unsupported session version %d (want %d)", h.Version, currentVersion)
		}
	case entryMessage:
		var m agentcore.Message
		if err := json.Unmarshal(e.Data, &m); err != nil {
			return err
		}
		if m.Role == "" {
			return errors.New("message without a role")
		}
		s.Usage.Add(m.Usage)
		s.Messages = append(s.Messages, m)
	case entryCompaction:
		var c compaction
		if err := json.Unmarshal(e.Data, &c); err != nil {
			return err
		}
		s.Usage.Add(c.Usage)
		s.Messages = c.Messages
	case entryModel:
		return json.Unmarshal(e.Data, &s.Model)
	default:
		return fmt.Errorf("unknown entry kind %q", e.Kind)
	}
	return nil
}

// Append records a message appended to the history.
func (s *Store) Append(m agentcore.Message) error {
	return s.append(entryMessage, m)
}

// AppendCompaction records a compaction replacing the whole history.
func (s *Store) AppendCompaction(c *agentcore.Compaction) error {
	return s.append(entryCompaction, compaction{Messages: c.Messages, Usage: c.Usage})
}

// AppendModel records the model the session runs on from here on.
func (s *Store) AppendModel(m Model) error {
	return s.append(entryModel, m)
}

// Header returns the session header.
func (s *Store) Header() Header { return s.header }

// Path returns the session file path.
func (s *Store) Path() string { return s.path }

// Close closes the session file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

func (s *Store) append(kind entryKind, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s entry: %w", kind, err)
	}
	line, err := json.Marshal(entry{Kind: kind, Timestamp: time.Now(), Data: raw})
	if err != nil {
		return fmt.Errorf("marshal %s entry: %w", kind, err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return errors.New("session store is closed")
	}
	offset, err := s.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("locate append offset: %w", err)
	}
	if _, err := s.file.Write(line); err != nil {
		// A short write leaves a partial, unterminated line. Left in place, the
		// next successful append splices onto it into a newline-terminated but
		// malformed line that recovery cannot skip — permanently unresumable.
		// Roll back to the pre-write offset so the torn bytes never durably land.
		if _, seekErr := s.file.Seek(offset, io.SeekStart); seekErr == nil {
			_ = s.file.Truncate(offset)
		}
		return err
	}
	return nil
}

func generateID() string {
	b := make([]byte, 4)
	rand.Read(b) // never fails since Go 1.24
	return hex.EncodeToString(b)
}
