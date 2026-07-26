package agent

import (
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/telemetry"
)

// turnState owns per-turn observation: in-flight/recent tool calls, turn
// outcome counters, the last run summary/reminder, and the dirty sequence
// consumed by PostStopValidation. One lock keeps a tool-end event's pending
// consumption, ring append, counter bump, and dirty bump atomic.
//
// dirtySeq crosses groups with generation (s.mu) in runPostStopValidation:
// both sides of that CAS tolerate a session switch in between because reset()
// zeroes dirtySeq and clearDirtyIfUnchanged only clears on an exact match —
// see the comments at the call sites.
type turnState struct {
	mu sync.Mutex

	pendingToolCalls map[string]pendingToolCall
	recentToolCalls  []toolCallFingerprint
	currentTurn      TurnOutcomeSnapshot
	lastTurn         TurnOutcomeSnapshot
	lastRunSummary   *agentcore.RunSummary
	lastReminder     *ReminderSnapshot
	dirtySeq         uint64 // incremented each time a repo-mutating tool succeeds; hook goroutine captures this and only clears if unchanged
}

func (t *turnState) trackStart(id string, call pendingToolCall) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pendingToolCalls == nil {
		t.pendingToolCalls = make(map[string]pendingToolCall)
	}
	t.pendingToolCalls[id] = call
}

// trackEnd consumes the pending entry, appends to the recent ring, updates
// the turn counters, and bumps dirtySeq — one critical section, so a
// concurrent finalize/reset can never observe a half-recorded tool call.
// Returns the resolved record plus a detached copy of the ring for the
// repeated-call detector.
func (t *turnState) trackEnd(id, tool string, success bool) (toolCallFingerprint, []toolCallFingerprint) {
	t.mu.Lock()
	defer t.mu.Unlock()
	call := pendingToolCall{Tool: tool}
	if pending, ok := t.pendingToolCalls[id]; ok {
		call = pending
		delete(t.pendingToolCalls, id)
	}

	record := toolCallFingerprint{
		Tool:      call.Tool,
		ArgsHash:  call.ArgsHash,
		Success:   success,
		Timestamp: time.Now(),
	}
	t.recentToolCalls = append(t.recentToolCalls, record)
	if len(t.recentToolCalls) > maxRecentToolCalls {
		t.recentToolCalls = append([]toolCallFingerprint(nil), t.recentToolCalls[len(t.recentToolCalls)-maxRecentToolCalls:]...)
	}

	name := record.Tool
	if isReadOnlyExplorationTool(name) {
		t.currentTurn.ReadOnlyToolCalls++
	}
	if isWriteLikeTool(name) {
		t.currentTurn.WriteLikeToolCalls++
	}
	if isTaskMutationTool(name) {
		t.currentTurn.TaskMutations++
	}
	if isCodeEditTool(name) {
		t.currentTurn.CodeEditToolCalls++
	}
	if success && isRepoMutatingTool(name) {
		t.dirtySeq++
	}

	return record, append([]toolCallFingerprint(nil), t.recentToolCalls...)
}

func (t *turnState) markAssistantResponded() {
	t.mu.Lock()
	t.currentTurn.AssistantResponded = true
	t.mu.Unlock()
}

func (t *turnState) beginTurn() {
	t.mu.Lock()
	t.currentTurn = TurnOutcomeSnapshot{}
	t.mu.Unlock()
}

func (t *turnState) finalizeTurn() {
	t.mu.Lock()
	t.lastTurn = t.currentTurn
	t.currentTurn = TurnOutcomeSnapshot{}
	t.mu.Unlock()
}

func (t *turnState) lastTurnSnapshot() TurnOutcomeSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastTurn
}

// lastReminderAndTurn reads both in one critical section — the goal
// no-activity check pairs the reminder that started the turn with that same
// turn's outcome; reading them separately could mix adjacent turns.
func (t *turnState) lastReminderAndTurn() (*ReminderSnapshot, TurnOutcomeSnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastReminder, t.lastTurn
}

func (t *turnState) recordReminderSnapshot(kind RuntimeReminderKind, mode string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastReminder = &ReminderSnapshot{
		Kind:      kind,
		Mode:      mode,
		Timestamp: time.Now(),
	}
}

func (t *turnState) lastReminderSnapshot() (ReminderSnapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastReminder == nil {
		return ReminderSnapshot{}, false
	}
	return *t.lastReminder, true
}

func (t *turnState) setRunSummary(summary agentcore.RunSummary) {
	t.mu.Lock()
	t.lastRunSummary = &summary
	t.mu.Unlock()
}

func (t *turnState) runSummarySnapshot() (agentcore.RunSummary, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastRunSummary == nil {
		return agentcore.RunSummary{}, false
	}
	return *t.lastRunSummary, true
}

// validationSnapshot returns what runPostStopValidation needs in one read.
func (t *turnState) validationSnapshot() (*agentcore.RunSummary, uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastRunSummary, t.dirtySeq
}

// clearDirtyIfUnchanged clears the dirty marker only when no new mutations
// happened since seq was captured (the hook validated exactly that state).
func (t *turnState) clearDirtyIfUnchanged(seq uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dirtySeq == seq {
		t.dirtySeq = 0
	}
}

func (t *turnState) recentSnapshot(limit int) []toolCallFingerprint {
	t.mu.Lock()
	defer t.mu.Unlock()
	if limit <= 0 || limit > len(t.recentToolCalls) {
		limit = len(t.recentToolCalls)
	}
	start := len(t.recentToolCalls) - limit
	return append([]toolCallFingerprint(nil), t.recentToolCalls[start:]...)
}

func (t *turnState) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingToolCalls = nil
	t.recentToolCalls = nil
	t.currentTurn = TurnOutcomeSnapshot{}
	t.lastTurn = TurnOutcomeSnapshot{}
	t.lastRunSummary = nil
	t.lastReminder = nil
	t.dirtySeq = 0
}

// runState owns per-run bookkeeping: the active telemetry span and the retry
// attempt counter. Both live for exactly one agent run and never co-occur
// with generation logic, so they need none of the session lock.
type runState struct {
	mu           sync.Mutex
	activeRun    *telemetry.Run
	retryAttempt int
}

func (r *runState) beginRun(run *telemetry.Run) (previous *telemetry.Run) {
	r.mu.Lock()
	defer r.mu.Unlock()
	previous = r.activeRun
	r.activeRun = run
	return previous
}

// rollbackRun restores previous only when run is still the active one — a
// concurrent successful begin must not be clobbered by a failed one.
func (r *runState) rollbackRun(run, previous *telemetry.Run) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activeRun == run {
		r.activeRun = previous
	}
}

// endRun takes the active span (nil when none) and clears it.
func (r *runState) endRun() *telemetry.Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.activeRun
	r.activeRun = nil
	return run
}

func (r *runState) setRetryAttempt(attempt int) {
	r.mu.Lock()
	r.retryAttempt = attempt
	r.mu.Unlock()
}

func (r *runState) takeRetryAttempt() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	attempt := r.retryAttempt
	r.retryAttempt = 0
	return attempt
}
