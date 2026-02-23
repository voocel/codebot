package storage

import (
	"encoding/json"
	"time"
)

// EntryKind identifies the type of a JSONL entry.
type EntryKind string

const (
	EntryHeader         EntryKind = "header"
	EntryMessage        EntryKind = "message"
	EntryModelChange    EntryKind = "model_change"
	EntryCompaction     EntryKind = "compaction"
	EntryThinkingChange EntryKind = "thinking_change"
	EntrySessionInfo    EntryKind = "session_info"
)

// Entry is a single JSONL line in the session file.
type Entry struct {
	Kind      EntryKind       `json:"kind"`
	ID        string          `json:"id"`
	ParentID  string          `json:"parent_id,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// Header is the first line of a session file.
type Header struct {
	Version   int       `json:"version"`
	SessionID string    `json:"session_id"`
	Name      string    `json:"name,omitempty"`
	Cwd       string    `json:"cwd"`
	Created   time.Time `json:"created"`
}

// ModelChange records a model switch event.
type ModelChange struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ThinkingLevelChange records a thinking level switch event.
type ThinkingLevelChange struct {
	Level string `json:"level"`
}

// Compaction stores a compaction summary and optionally kept messages.
type Compaction struct {
	Summary  string            `json:"summary"`
	Messages []json.RawMessage `json:"messages,omitempty"`
}

// SessionInfo is a summary of a session for listing.
type SessionInfo struct {
	ID           string
	Name         string
	Path         string
	Cwd          string
	Created      time.Time
	Updated      time.Time
	MessageCount int
	FirstMessage string // first user message, truncated to 80 chars
}
