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
	EntryPlanState      EntryKind = "plan_state"
	EntryLLMCall        EntryKind = "llm_call"
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

// PlanStateEntry records a plan-mode phase transition.
type PlanStateEntry struct {
	Phase   string `json:"phase"`
	Slug    string `json:"slug,omitempty"`
	PreMode string `json:"pre_mode,omitempty"`
}

// LLMCallEntry is a per-turn observability record for a single LLM response.
// Emitted once per assistant message_end, independent of the message itself
// so that downstream can diagnose cache hits, latency, and provider without
// re-parsing the message payload.
type LLMCallEntry struct {
	Provider            string          `json:"provider"`
	Model               string          `json:"model"`
	InputTokens         int             `json:"input_tokens"`
	OutputTokens        int             `json:"output_tokens"`
	CacheReadTokens     int             `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int             `json:"cache_creation_tokens,omitempty"`
	TotalTokens         int             `json:"total_tokens,omitempty"`
	LatencyMs           int64           `json:"latency_ms,omitempty"`
	StopReason          string          `json:"stop_reason,omitempty"`
	ThinkingLevel       string          `json:"thinking_level,omitempty"`
	CacheBreak          *CacheBreakInfo `json:"cache_break,omitempty"`
}

// CacheBreakInfo is attached to an LLMCallEntry when the prompt cache hit
// rate unexpectedly dropped relative to the previous turn. It captures why
// the cache likely invalidated so the session can be diagnosed after-the-
// fact without replaying the full request.
type CacheBreakInfo struct {
	PrevCacheReadTokens int     `json:"prev_cache_read_tokens"`
	CurrCacheReadTokens int     `json:"curr_cache_read_tokens"`
	DropAbsolute        int     `json:"drop_absolute"`
	DropFraction        float64 `json:"drop_fraction"`
	// SystemChanged is true when either the frozen prefix or the dynamic
	// tail of the system blocks changed. Kept for backwards compatibility
	// with existing JSONL — prefer the finer-grained fields below.
	SystemChanged  bool   `json:"system_changed,omitempty"`
	FrozenChanged  bool   `json:"frozen_system_changed,omitempty"`
	DynamicChanged bool   `json:"dynamic_system_changed,omitempty"`
	ToolsChanged   bool   `json:"tools_changed,omitempty"`
	Note           string `json:"note,omitempty"`
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
