package agent

import (
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/goal"
)

type RuntimeReminderKind string

const (
	ReminderRepeatToolCall     RuntimeReminderKind = "repeat_tool_call"
	ReminderPostStopValidation RuntimeReminderKind = "post_stop_validation"
	ReminderSkillPaths         RuntimeReminderKind = "skill_paths"
	ReminderTaskManagement     RuntimeReminderKind = "task_management"
	ReminderPlanMode           RuntimeReminderKind = "plan_mode"
	ReminderGoal               RuntimeReminderKind = "goal"
	ReminderHookContext        RuntimeReminderKind = "hook_context"
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
	Kind           CompactionKind
	Strategy       string
	Reason         string
	Changed        bool
	TokensBefore   int
	TokensAfter    int
	CompactedCount int
	KeptCount      int
	SplitTurn      bool
	Timestamp      time.Time
}

// SessionEventType identifies a session-level event.
type SessionEventType string

const (
	// SEAgentEvent wraps an agentcore.Event transparently.
	SEAgentEvent SessionEventType = "agent_event"

	// Session lifecycle events
	SEAutoCompactionStart    SessionEventType = "auto_compaction_start"
	SEAutoCompactionEnd      SessionEventType = "auto_compaction_end"
	SEAutoRetryStart         SessionEventType = "auto_retry_start"
	SEAutoRetryEnd           SessionEventType = "auto_retry_end"
	SEModelChanged           SessionEventType = "model_changed"
	SEReasoningEffortChanged SessionEventType = "reasoning_effort_changed"
	SESessionSwitched        SessionEventType = "session_switched"
	SERuntimeReminder        SessionEventType = "runtime_reminder"
	SEGoalUpdated            SessionEventType = "goal_updated"
	SEGoalCleared            SessionEventType = "goal_cleared"
	SEError                  SessionEventType = "session_error"
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
	Goal         goal.State
	GoalPrevious goal.State

	// Retry fields (populated for SEAutoRetryStart / SEAutoRetryEnd)
	RetryAttempt int
	RetryMax     int
	RetryDelay   time.Duration
	RetrySuccess bool

	// Compaction reason: "overflow" or "threshold"
	CompactionReason   string
	CompactionKind     CompactionKind
	CompactionStrategy string
	CompactionChanged  bool
	TokensBefore       int
	TokensAfter        int
	CompactedCount     int
	KeptCount          int
	SplitTurn          bool
}

// eventBus fans SessionEvents out to subscribers in registration order
// (acp/ui attach in a fixed sequence and rely on that ordering). Entries are
// id-keyed: the unsubscribe closure captures the id, not a slice index, so
// removals can compact the slice without invalidating other closures.
// The zero value is ready to use.
type eventBus struct {
	mu     sync.RWMutex
	nextID uint64
	subs   []eventSub
}

type eventSub struct {
	id uint64
	fn func(SessionEvent)
}

func (b *eventBus) subscribe(fn func(SessionEvent)) func() {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs = append(b.subs, eventSub{id: id, fn: fn})
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i := range b.subs {
			if b.subs[i].id == id {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				break
			}
		}
	}
}

// dispatch snapshots the subscriber list under RLock and invokes outside it,
// so a callback may subscribe/unsubscribe without deadlocking.
func (b *eventBus) dispatch(ev SessionEvent) {
	b.mu.RLock()
	fns := make([]func(SessionEvent), len(b.subs))
	for i := range b.subs {
		fns[i] = b.subs[i].fn
	}
	b.mu.RUnlock()
	for _, fn := range fns {
		fn(ev)
	}
}
