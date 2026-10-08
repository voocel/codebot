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
	"slices"
	"sync"
	"time"

	"github.com/voocel/agentcore"
)

const currentVersion = 5

// Store is safe for concurrent use.
type Store struct {
	path   string
	header Header
	mu     sync.Mutex
	file   *os.File
}

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

// open cuts off a torn final line left by a crash before appending.
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
		s.Messages, s.Checkpoints = c.Messages, nil
	case entryModel:
		return json.Unmarshal(e.Data, &s.Model)
	case entryCheckpoint:
		var c Checkpoint
		if err := json.Unmarshal(e.Data, &c); err != nil {
			return err
		}
		if c.At < 0 || c.At > len(s.Messages) {
			return fmt.Errorf("checkpoint at %d of %d messages", c.At, len(s.Messages))
		}
		s.Checkpoints = append(s.Checkpoints, c)
	case entryRewind:
		var r rewind
		if err := json.Unmarshal(e.Data, &r); err != nil {
			return err
		}
		if r.Keep < 0 || r.Keep > len(s.Messages) {
			return fmt.Errorf("rewind to %d of %d messages", r.Keep, len(s.Messages))
		}
		s.Messages = s.Messages[:r.Keep]
		s.Checkpoints = slices.DeleteFunc(s.Checkpoints, func(c Checkpoint) bool { return c.At >= r.Keep })
	default:
		return fmt.Errorf("unknown entry kind %q", e.Kind)
	}
	return nil
}

func (s *Store) Append(m agentcore.Message) error {
	return s.append(entryMessage, m)
}

func (s *Store) AppendCompaction(c *agentcore.Compaction) error {
	return s.append(entryCompaction, compaction{Messages: c.Messages, Usage: c.Usage})
}

func (s *Store) AppendCheckpoint(c Checkpoint) error { return s.append(entryCheckpoint, c) }

func (s *Store) AppendRewind(keep int) error { return s.append(entryRewind, rewind{Keep: keep}) }

func (s *Store) AppendModel(m Model) error {
	return s.append(entryModel, m)
}

func (s *Store) Header() Header { return s.header }

func (s *Store) Path() string { return s.path }

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
		// Roll back a short write. Otherwise the next append would join the
		// torn bytes into a terminated but malformed line, which recovery
		// cannot skip, and the session could never be resumed.
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
