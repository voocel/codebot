package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// Manager manages session files in a directory.
type Manager struct {
	dir string
}

// NewManager creates a Manager for the given sessions directory.
func NewManager(dir string) *Manager {
	return &Manager{dir: dir}
}

// List returns all sessions sorted by updated time (newest first). Files
// that are not readable sessions of the current version are skipped.
func (m *Manager) List() ([]SessionInfo, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	var sessions []SessionInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := readSessionInfo(filepath.Join(m.dir, e.Name()))
		if err != nil {
			continue
		}
		sessions = append(sessions, info)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Updated.After(sessions[j].Updated)
	})
	return sessions, nil
}

// Open opens an existing session by ID and replays it.
func (m *Manager) Open(id string) (*Store, State, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, State{}, fmt.Errorf("open session: %w", err)
	}
	suffix := "_" + id + ".jsonl"
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			return open(filepath.Join(m.dir, e.Name()))
		}
	}
	return nil, State{}, fmt.Errorf("session %q not found", id)
}

// Create creates a new session.
func (m *Manager) Create(cwd string) (*Store, error) {
	return create(m.dir, cwd)
}

func readSessionInfo(path string) (SessionInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionInfo{}, err
	}
	defer f.Close()

	info := SessionInfo{Path: path}
	first := true
	_, err = scanJSONLines(f, func(line []byte) error {
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			return err
		}
		if first {
			first = false
			var h Header
			if e.Kind != entryHeader || json.Unmarshal(e.Data, &h) != nil {
				return errors.New("not a session file")
			}
			if h.Version != currentVersion {
				return fmt.Errorf("unsupported session version %d", h.Version)
			}
			info.ID, info.Cwd, info.Created, info.Updated = h.SessionID, h.Cwd, h.Created, h.Created
		}
		if e.Timestamp.After(info.Updated) {
			info.Updated = e.Timestamp
		}
		if e.Kind != entryMessage {
			return nil
		}
		info.MessageCount++
		if info.FirstMessage == "" {
			info.FirstMessage = userText(e.Data)
		}
		return nil
	})
	if err == nil && first {
		err = errors.New("empty file")
	}
	return info, err
}

// userText returns the text a user typed in a message entry, truncated for
// listing; "" for other messages and for messages the harness injected,
// which carry a Kind.
func userText(data json.RawMessage) string {
	var msg agentcore.Message
	if json.Unmarshal(data, &msg) != nil || msg.Role != litellm.RoleUser || msg.Kind != "" {
		return ""
	}
	// Take the last text block: reminders are prepended, the user's actual
	// input is always the final text block.
	var text string
	for _, b := range msg.Blocks {
		if t, ok := b.(litellm.TextBlock); ok && t.Text != "" {
			text = t.Text
		}
	}
	if r := []rune(text); len(r) > 80 {
		text = string(r[:77]) + "..."
	}
	return text
}
