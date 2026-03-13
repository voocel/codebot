package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Manager manages session files in a directory.
type Manager struct {
	Dir string
}

// NewManager creates a Manager for the given sessions directory.
func NewManager(dir string) *Manager {
	return &Manager{Dir: dir}
}

// List returns all sessions sorted by updated time (newest first).
func (m *Manager) List() ([]SessionInfo, error) {
	entries, err := os.ReadDir(m.Dir)
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
		path := filepath.Join(m.Dir, e.Name())
		info, err := readSessionInfo(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[session] skip %s: %v\n", e.Name(), err)
			continue
		}
		sessions = append(sessions, info)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Updated.After(sessions[j].Updated)
	})
	return sessions, nil
}

// MostRecent returns the most recently updated session.
func (m *Manager) MostRecent() (*SessionInfo, error) {
	sessions, err := m.List()
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	return &sessions[0], nil
}

// Open opens an existing session by ID.
func (m *Manager) Open(id string) (*Store, error) {
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}
	suffix := "_" + id + ".jsonl"
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			return open(filepath.Join(m.Dir, e.Name()))
		}
	}
	return nil, fmt.Errorf("session %q not found", id)
}

// OpenPath opens a session by file path.
func (m *Manager) OpenPath(path string) (*Store, error) {
	return open(path)
}

// Create creates a new session.
func (m *Manager) Create(cwd string) (*Store, error) {
	return create(m.Dir, cwd)
}

func readSessionInfo(path string) (SessionInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionInfo{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		return SessionInfo{}, fmt.Errorf("empty file")
	}

	var entry Entry
	if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
		return SessionInfo{}, err
	}
	if entry.Kind != EntryHeader {
		return SessionInfo{}, fmt.Errorf("not a session file")
	}

	var h Header
	if err := json.Unmarshal(entry.Data, &h); err != nil {
		return SessionInfo{}, err
	}
	if h.Version != currentVersion {
		return SessionInfo{}, fmt.Errorf("unsupported version: %d", h.Version)
	}

	// Scan all entries for name, message count, first user message, and latest timestamp.
	name := h.Name
	var messageCount int
	var firstMessage string
	// Seed with header entry's timestamp (so even header-only sessions get a valid Updated).
	lastTimestamp := entry.Timestamp
	if lastTimestamp.IsZero() {
		lastTimestamp = h.Created
	}

	for scanner.Scan() {
		var e Entry
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}

		if !e.Timestamp.IsZero() && e.Timestamp.After(lastTimestamp) {
			lastTimestamp = e.Timestamp
		}

		switch e.Kind {
		case EntrySessionInfo:
			var info map[string]string
			if json.Unmarshal(e.Data, &info) == nil {
				if n, ok := info["name"]; ok {
					name = n
				}
			}
		case EntryMessage:
			messageCount++
			if firstMessage == "" {
				var msg struct {
					Role     string `json:"role"`
					Content  []struct {
						Text string `json:"text"`
					} `json:"content"`
					Metadata map[string]any `json:"metadata,omitempty"`
				}
				if json.Unmarshal(e.Data, &msg) == nil && msg.Role == "user" && msg.Metadata["injected"] != true {
					// Take the last text block: reminders are prepended,
					// the user's actual input is always the final text block.
					for _, c := range msg.Content {
						if c.Text != "" {
							firstMessage = c.Text
						}
					}
					if len(firstMessage) > 80 {
						firstMessage = firstMessage[:77] + "..."
					}
				}
			}
		}
	}

	info := SessionInfo{
		ID:           h.SessionID,
		Name:         name,
		Path:         path,
		Cwd:          h.Cwd,
		Created:      h.Created,
		MessageCount: messageCount,
		FirstMessage: firstMessage,
	}
	if !lastTimestamp.IsZero() {
		info.Updated = lastTimestamp
	}
	return info, nil
}
