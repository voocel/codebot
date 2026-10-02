package storage

import (
	"encoding/json"
	"time"

	"github.com/voocel/agentcore"
)

// entryKind identifies the type of a JSONL entry.
type entryKind string

const (
	entryHeader     entryKind = "header"
	entryMessage    entryKind = "message"    // appends a message to the history
	entryCompaction entryKind = "compaction" // replaces the whole history
	entryModel      entryKind = "model"      // records the model the session runs on
)

// entry is a single JSONL line in the session file.
type entry struct {
	Kind      entryKind       `json:"kind"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// Header is the first line of a session file.
type Header struct {
	Version   int       `json:"version"`
	SessionID string    `json:"session_id"`
	Cwd       string    `json:"cwd"`
	Created   time.Time `json:"created"`
}

// Model is a model selection as recorded in the log.
type Model struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort,omitempty"`
}

// compaction is the data of a compaction entry.
type compaction struct {
	Messages []agentcore.Message `json:"messages"`
}

// State is what a session log replays to.
type State struct {
	Messages []agentcore.Message
	// Model is the last recorded model selection; zero when none was recorded.
	Model Model
	// Usage sums every recorded response, including those a compaction later
	// replaced.
	Usage agentcore.Usage
}

// SessionInfo is a summary of a session for listing.
type SessionInfo struct {
	ID           string
	Path         string
	Cwd          string
	Created      time.Time
	Updated      time.Time
	MessageCount int
	FirstMessage string // first user message, truncated to 80 runes
}
