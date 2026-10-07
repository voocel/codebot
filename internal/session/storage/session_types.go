package storage

import (
	"encoding/json"
	"time"

	"github.com/voocel/agentcore"
)

type entryKind string

const (
	entryHeader     entryKind = "header"
	entryMessage    entryKind = "message"    // appends a message to the history
	entryCompaction entryKind = "compaction" // replaces the whole history
	entryModel      entryKind = "model"      // records the model the session runs on
)

type entry struct {
	Kind      entryKind       `json:"kind"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// Header is the first line of the file.
type Header struct {
	Version   int       `json:"version"`
	SessionID string    `json:"session_id"`
	Cwd       string    `json:"cwd"`
	Created   time.Time `json:"created"`
}

type Model struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort,omitempty"`
}

type compaction struct {
	Messages []agentcore.Message `json:"messages"`
	Usage    *agentcore.Usage    `json:"usage,omitempty"`
}

type State struct {
	Messages []agentcore.Message
	Model    Model // zero when none was recorded
	// Usage includes responses that a compaction later replaced, and the
	// compactions themselves.
	Usage agentcore.Usage
}

type SessionInfo struct {
	ID           string
	Path         string
	Cwd          string
	Created      time.Time
	Updated      time.Time
	MessageCount int    // messages of the conversation, not those the harness added
	FirstMessage string // first user message, truncated to 80 runes
}
