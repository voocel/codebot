package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/voocel/agentcore"
)

const (
	maxRecentToolCalls        = 8
	maxRecentErrors           = 10
	repeatedToolCallThreshold = 4
)

type sessionRuntimePolicy struct {
	session *Session
}

type toolCallFingerprint struct {
	Tool      string
	ArgsHash  string
	Success   bool
	Timestamp time.Time
}

type pendingToolCall struct {
	Tool     string
	ArgsHash string
}

type TurnOutcomeSnapshot struct {
	AssistantResponded bool
	ReadOnlyToolCalls  int
	WriteLikeToolCalls int
	TaskMutations      int
	CodeEditToolCalls  int
}

func newSessionRuntimePolicy(session *Session) *sessionRuntimePolicy {
	return &sessionRuntimePolicy{session: session}
}

func (p *sessionRuntimePolicy) beforeUserPrompt(blocks []agentcore.ContentBlock) {
	_ = blocks
	p.queueDateChangeReminder()
	p.queueTaskManagementPromptReminder()
	p.queuePlanModePromptReminder()
}

// queueDateChangeReminder corrects system block 1 after a session outlives
// midnight. The date is baked into that block so it costs nothing per turn;
// this is the one-line patch for the rare rollover, mirroring how plan-mode
// reminders refresh a contract that lives elsewhere.
func (p *sessionRuntimePolicy) queueDateChangeReminder() {
	today := time.Now().Format("2006-01-02")
	if !p.session.reminders.takeDateChange(today) {
		return
	}
	p.session.queueRuntimeReminder(
		"date_change:"+today,
		ReminderDateChange,
		wrapReminder("The date has changed since this session started. Today's date is now "+today+". The date in your environment block is stale; use this one."),
	)
}

func (p *sessionRuntimePolicy) handleEvent(ev agentcore.Event) {
	switch ev.Type {
	case agentcore.EventToolExecStart:
		p.trackToolStart(ev)
	case agentcore.EventToolExecEnd:
		p.trackToolEnd(ev)
	case agentcore.EventMessageEnd:
		if msg, ok := ev.Message.(agentcore.Message); ok {
			p.handleMessageEnd(msg)
		}
	}
}

func (p *sessionRuntimePolicy) afterAgentEnd() {
	if p.runPostStopValidation() {
		return
	}
	p.continueGoalIfActive()
}

func (p *sessionRuntimePolicy) trackToolStart(ev agentcore.Event) {
	if ev.ToolID == "" || ev.Tool == "" {
		return
	}
	p.session.turn.trackStart(ev.ToolID, pendingToolCall{
		Tool:     ev.Tool,
		ArgsHash: hashToolArgs(ev.Args),
	})
}

func (p *sessionRuntimePolicy) trackToolEnd(ev agentcore.Event) {
	if ev.Tool == "" {
		return
	}
	record, recent := p.session.turn.trackEnd(ev.ToolID, ev.Tool, !ev.IsError)
	p.detectRepeatedCalls(record, recent)
}

func (p *sessionRuntimePolicy) detectRepeatedCalls(current toolCallFingerprint, recent []toolCallFingerprint) {
	if current.Tool == "" {
		return
	}

	sameCount := 0
	for i := len(recent) - 1; i >= 0; i-- {
		record := recent[i]
		if record.Tool != current.Tool || record.ArgsHash != current.ArgsHash {
			break
		}
		sameCount++
	}

	if sameCount >= repeatedToolCallThreshold {
		p.session.deliverRuntimeReminder(
			"repeat_tool_call:"+current.Tool+":"+current.ArgsHash,
			ReminderRepeatToolCall,
			"<system-reminder>\nYou are repeatedly calling the same tool with effectively the same arguments. Summarize what you already know, what is still missing, and your next hypothesis before making the same call again.\n</system-reminder>",
		)
	}
}

func (p *sessionRuntimePolicy) handleMessageEnd(msg agentcore.Message) {
	if msg.Role != agentcore.RoleAssistant {
		return
	}

	s := p.session
	if s.deps.taskStore == nil {
		return
	}
	snap := s.deps.taskStore.Snapshot()
	if key, reminder, ok := taskManagementReminderBeforeStop(msg, snap); ok {
		s.deliverRuntimeReminder(
			key,
			ReminderTaskManagement,
			reminder,
		)
	}
}

func (p *sessionRuntimePolicy) queueTaskManagementPromptReminder() {
	s := p.session
	if s.deps.taskStore == nil || !hasToolNamed(s.prompt.activeToolsSnapshot(), "task_update") {
		return
	}

	snap := s.deps.taskStore.Snapshot()
	if key, reminder, ok := taskManagementReminderForNextPrompt(s.deps.agent.Messages(), snap); ok {
		s.queueRuntimeReminder(key, ReminderTaskManagement, reminder)
	}
}

func (p *sessionRuntimePolicy) queuePlanModePromptReminder() {
	s := p.session
	sig := s.currentPlanModeSignal()
	switch {
	case sig.Active:
		if key, reminder, ok := planModeReminderForNextPrompt(s.deps.agent.Messages(), sig.PlanFilePath); ok {
			s.queueRuntimeReminder(key, ReminderPlanMode, reminder)
		}
	case sig.JustCancelled:
		key, reminder := planModeCancelledReminderForNextPrompt()
		s.queueRuntimeReminder(key, ReminderPlanMode, reminder)
	}
}

func hashToolArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	compacted := &bytes.Buffer{}
	if err := json.Compact(compacted, raw); err != nil {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:8])
	}
	sum := sha256.Sum256(compacted.Bytes())
	return hex.EncodeToString(sum[:8])
}

func isReadOnlyExplorationTool(name string) bool {
	switch name {
	case "read", "glob", "grep", "ls":
		return true
	default:
		return false
	}
}

func hasToolNamed(tools []agentcore.Tool, name string) bool {
	for _, tool := range tools {
		if tool != nil && tool.Name() == name {
			return true
		}
	}
	return false
}

func (s *Session) beginTurn() {
	s.mu.Lock()
	s.backgroundWakeSuppressed = false
	s.mu.Unlock()
	s.turn.beginTurn()
	s.reminders.resetTurnDelivery()
	// Checkpoint the workspace before the turn touches files. Runs outside the
	// lock (git I/O) and is best-effort — see snapshotTurnStart.
	s.snapshotTurnStart()
}

func (s *Session) recordAssistantTurnMessage(_ agentcore.Message) {
	s.turn.markAssistantResponded()
}

func (s *Session) finalizeTurnOutcome() {
	s.turn.finalizeTurn()
	// Dedup sets only — pendingContinue must survive; it is consumed by
	// continuePendingReminder after the run ends.
	s.reminders.clearDeliveryDedup()
}

func (p *sessionRuntimePolicy) continuePendingReminder() bool {
	s := p.session
	// Read the generation before consuming the flag. Should a session switch
	// land between the two, this gen is the pre-switch one and
	// continueIfCurrentGeneration bails; the opposite order would capture the
	// new generation and resume the reminder into the wrong session.
	s.mu.Lock()
	gen := s.generation
	s.mu.Unlock()
	if !s.reminders.takePendingContinue() {
		return false
	}

	go func() {
		if err := s.continueIfCurrentGeneration(gen); err != nil {
			if errors.Is(err, errStaleSessionGeneration) {
				return
			}
			// A concurrent Reset/SwitchSession rejects the launch outright
			// (ErrRunsHeld) or, when it completed between our generation
			// sample and the launch, leaves nothing to continue
			// (ErrBadContinuation / ErrNoMessages) — the steered reminder
			// died with the old session, which is not an error. A user prompt
			// winning the race (ErrAlreadyRunning) is equally benign: its run
			// consumes the steered reminder via the steering poll.
			if errors.Is(err, agentcore.ErrRunsHeld) ||
				errors.Is(err, agentcore.ErrAlreadyRunning) ||
				errors.Is(err, agentcore.ErrBadContinuation) ||
				errors.Is(err, agentcore.ErrNoMessages) {
				return
			}
			s.clearSkillDelta()
			s.emit(SessionEvent{
				Type:  SEError,
				Error: fmt.Errorf("runtime reminder continue: %w", err),
			})
		}
	}()
	return true
}

func (s *Session) LastTurnOutcome() TurnOutcomeSnapshot {
	return s.turn.lastTurnSnapshot()
}

func isTaskMutationTool(name string) bool {
	switch name {
	case "task_create", "task_update":
		return true
	default:
		return false
	}
}

func isWriteLikeTool(name string) bool {
	switch name {
	case "bash", "task_create", "task_update", "cron_create", "cron_delete", "write", "edit", "replace", "apply_patch", "delete":
		return true
	default:
		return false
	}
}

// isCodeEditTool returns true for tools that modify repository files.
func isCodeEditTool(name string) bool {
	switch name {
	case "write", "edit", "replace", "apply_patch", "delete":
		return true
	default:
		return false
	}
}

// isRepoMutatingTool returns true for tools that may modify repository state.
// This is a superset of isCodeEditTool: bash commands can also alter files
// (e.g. sed -i, rm, git checkout), so they should mark the repo as dirty
// for PostStopValidation purposes.
func isRepoMutatingTool(name string) bool {
	switch name {
	case "bash", "write", "edit", "replace", "apply_patch", "delete":
		return true
	default:
		return false
	}
}

func (p *sessionRuntimePolicy) continueGoalIfActive() bool {
	s := p.session
	if p.lastGoalContinuationHadNoActivity() {
		return false
	}
	sig := s.currentGoalSignal()
	if sig.Err != nil {
		s.emit(SessionEvent{
			Type:  SEError,
			Error: fmt.Errorf("goal continuation: %w", sig.Err),
		})
		return false
	}
	if !sig.Active || sig.Key == "" || sig.Reminder == "" {
		return false
	}
	if s.reminders.hasQueued() {
		return false
	}
	s.continueWithRuntimeReminder(sig.Key, ReminderGoal, sig.Reminder)
	return true
}

func (p *sessionRuntimePolicy) lastGoalContinuationHadNoActivity() bool {
	lastReminder, outcome := p.session.turn.lastReminderAndTurn()

	if lastReminder == nil || lastReminder.Kind != ReminderGoal {
		return false
	}
	return !outcome.AssistantResponded &&
		outcome.ReadOnlyToolCalls == 0 &&
		outcome.WriteLikeToolCalls == 0 &&
		outcome.TaskMutations == 0 &&
		outcome.CodeEditToolCalls == 0
}

func (p *sessionRuntimePolicy) continueGoalIfActiveForGeneration(gen uint64) bool {
	s := p.session
	s.mu.Lock()
	stale := s.generation != gen
	s.mu.Unlock()
	if stale {
		return false
	}
	return p.continueGoalIfActive()
}

// runPostStopValidation fires PostStopValidation hooks when the agent stops
// naturally and there are unverified code edits since the last successful hook
// run. It returns true when validation was launched asynchronously; callers
// should not start lower-priority continuation work until the hook finishes.
func (p *sessionRuntimePolicy) runPostStopValidation() bool {
	s := p.session
	if s.deps.hookRunner == nil {
		return false
	}
	summary, seq := s.turn.validationSnapshot()
	s.mu.Lock()
	gen := s.generation
	s.mu.Unlock()

	if summary == nil || summary.EndReason != agentcore.EndReasonStop {
		return false
	}
	if seq == 0 {
		return false
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		failOutput := s.deps.hookRunner.RunPostStopValidation(ctx)

		// Cross-group window (generation on s.mu, dirtySeq on turn.mu): a
		// switch landing between the two checks is harmless — reset() zeroes
		// dirtySeq, and clearDirtyIfUnchanged only clears on an exact match
		// with the pre-switch seq (which is never 0 here).
		s.mu.Lock()
		stale := s.generation != gen
		s.mu.Unlock()
		if stale {
			return // session switched; discard stale result
		}
		if failOutput == "" {
			// Only clear dirty if no new mutations happened while the hook was running.
			s.turn.clearDirtyIfUnchanged(seq)
			p.continueGoalIfActiveForGeneration(gen)
			return
		}

		s.continueWithRuntimeReminder(
			"post_stop_validation",
			ReminderPostStopValidation,
			fmt.Sprintf(
				"<system-reminder>\nThe PostStopValidation hook failed. Fix the problem based on the following output:\n%s\n</system-reminder>",
				failOutput,
			),
		)
	}()
	return true
}
