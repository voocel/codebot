package agent

import (
	"time"

	"github.com/voocel/agentcore"
)

type RuntimeReminderKind string

const (
	ReminderRepeatToolCall     RuntimeReminderKind = "repeat_tool_call"
	ReminderPostStopValidation RuntimeReminderKind = "post_stop_validation"
)

type CompactionKind string

const (
	CompactionKindMicro CompactionKind = "micro"
	CompactionKindFull  CompactionKind = "full"
	CompactionKindTrim  CompactionKind = "trim"
	CompactionKindPrune CompactionKind = "prune"
)

type ToolCallSnapshot struct {
	Tool      string
	ArgsHash  string
	Success   bool
	Timestamp time.Time
}

type ReminderSnapshot struct {
	Kind      RuntimeReminderKind
	Mode      string
	Timestamp time.Time
}

type CompactionSnapshot struct {
	Kind         CompactionKind
	Reason       string
	Changed      bool
	TokensBefore int
	TokensAfter  int
	Timestamp    time.Time
}

// SessionEventType identifies a session-level event.
type SessionEventType string

const (
	// SEAgentEvent wraps an agentcore.Event transparently.
	SEAgentEvent SessionEventType = "agent_event"

	// Session lifecycle events
	SEAutoCompactionStart SessionEventType = "auto_compaction_start"
	SEAutoCompactionEnd   SessionEventType = "auto_compaction_end"
	SEAutoRetryStart      SessionEventType = "auto_retry_start"
	SEAutoRetryEnd        SessionEventType = "auto_retry_end"
	SEModelChanged        SessionEventType = "model_changed"
	SEThinkingChanged     SessionEventType = "thinking_changed"
	SESessionSwitched     SessionEventType = "session_switched"
	SERuntimeReminder     SessionEventType = "runtime_reminder"
	SEError               SessionEventType = "session_error"
)

// SessionEvent extends agent events with session-level metadata.
// When Type == SEAgentEvent, AgentEvent is non-nil.
type SessionEvent struct {
	Type       SessionEventType
	AgentEvent *agentcore.Event

	// Session-level fields (populated based on Type)
	ModelName    string
	Provider     string
	Level        agentcore.ThinkingLevel
	SessionID    string
	Error        error
	Reminder     string
	ReminderKind RuntimeReminderKind

	// Retry fields (populated for SEAutoRetryStart / SEAutoRetryEnd)
	RetryAttempt int
	RetryMax     int
	RetryDelay   time.Duration
	RetrySuccess bool

	// Compaction reason: "overflow" or "threshold"
	CompactionReason  string
	CompactionKind    CompactionKind
	CompactionChanged bool
	TokensBefore      int
	TokensAfter       int
}
