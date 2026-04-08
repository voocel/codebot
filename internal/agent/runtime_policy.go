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
	p.runPostStopValidation()
}

func (p *sessionRuntimePolicy) trackToolStart(ev agentcore.Event) {
	if ev.ToolID == "" || ev.Tool == "" {
		return
	}
	p.session.mu.Lock()
	if p.session.pendingToolCalls == nil {
		p.session.pendingToolCalls = make(map[string]pendingToolCall)
	}
	p.session.pendingToolCalls[ev.ToolID] = pendingToolCall{
		Tool:     ev.Tool,
		ArgsHash: hashToolArgs(ev.Args),
	}
	p.session.mu.Unlock()
}

func (p *sessionRuntimePolicy) trackToolEnd(ev agentcore.Event) {
	if ev.Tool == "" {
		return
	}

	p.session.mu.Lock()
	call := pendingToolCall{Tool: ev.Tool}
	if pending, ok := p.session.pendingToolCalls[ev.ToolID]; ok {
		call = pending
		delete(p.session.pendingToolCalls, ev.ToolID)
	}

	record := toolCallFingerprint{
		Tool:      call.Tool,
		ArgsHash:  call.ArgsHash,
		Success:   !ev.IsError,
		Timestamp: time.Now(),
	}
	p.session.recentToolCalls = append(p.session.recentToolCalls, record)
	if len(p.session.recentToolCalls) > maxRecentToolCalls {
		p.session.recentToolCalls = append([]toolCallFingerprint(nil), p.session.recentToolCalls[len(p.session.recentToolCalls)-maxRecentToolCalls:]...)
	}
	recent := append([]toolCallFingerprint(nil), p.session.recentToolCalls...)
	p.session.recordTurnTool(record.Tool)
	if record.Success && isRepoMutatingTool(record.Tool) {
		p.session.dirtySeq++
	}
	p.session.mu.Unlock()

	p.detectRepeatedCalls(record, recent)
	p.detectTaskManagementGap()
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

func (p *sessionRuntimePolicy) detectTaskManagementGap() {
	s := p.session
	if s.taskStore == nil {
		return
	}

	s.mu.Lock()
	turn := s.currentTurn
	s.mu.Unlock()

	snap := s.taskStore.Snapshot()
	if key, reminder, ok := taskManagementReminderForTurn(turn, snap); ok {
		p.session.deliverRuntimeReminder(
			key,
			ReminderTaskManagement,
			reminder,
		)
	}
}

func (p *sessionRuntimePolicy) handleMessageEnd(msg agentcore.Message) {
	if msg.Role != agentcore.RoleAssistant {
		return
	}

	s := p.session
	if s.taskStore == nil {
		return
	}
	snap := s.taskStore.Snapshot()
	if key, reminder, ok := taskManagementReminderBeforeStop(msg, snap); ok {
		s.deliverRuntimeReminder(
			key,
			ReminderTaskManagement,
			reminder,
		)
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

func (s *Session) beginTurn() {
	s.mu.Lock()
	s.currentTurn = TurnOutcomeSnapshot{}
	s.steeredReminderKeys = make(map[string]struct{})
	s.autoResumeReminderKeys = make(map[string]struct{})
	s.pendingReminderContinue = false
	s.mu.Unlock()
}

func (s *Session) recordTurnTool(name string) {
	if isReadOnlyExplorationTool(name) {
		s.currentTurn.ReadOnlyToolCalls++
	}
	if isWriteLikeTool(name) {
		s.currentTurn.WriteLikeToolCalls++
	}
	if isTaskMutationTool(name) {
		s.currentTurn.TaskMutations++
	}
	if isCodeEditTool(name) {
		s.currentTurn.CodeEditToolCalls++
	}
}

func (s *Session) recordAssistantTurnMessage(_ agentcore.Message) {
	s.mu.Lock()
	s.currentTurn.AssistantResponded = true
	s.mu.Unlock()
}

func (s *Session) finalizeTurnOutcome() {
	s.mu.Lock()
	s.lastTurn = s.currentTurn
	s.currentTurn = TurnOutcomeSnapshot{}
	s.steeredReminderKeys = make(map[string]struct{})
	s.mu.Unlock()
}

func (p *sessionRuntimePolicy) continuePendingReminder() bool {
	s := p.session
	s.mu.Lock()
	pending := s.pendingReminderContinue
	s.pendingReminderContinue = false
	gen := s.generation
	s.mu.Unlock()
	if !pending {
		return false
	}

	go func() {
		if err := s.continueIfCurrentGeneration(gen); err != nil {
			if errors.Is(err, errStaleSessionGeneration) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return TurnOutcomeSnapshot{
		AssistantResponded: s.lastTurn.AssistantResponded,
		ReadOnlyToolCalls:  s.lastTurn.ReadOnlyToolCalls,
		WriteLikeToolCalls: s.lastTurn.WriteLikeToolCalls,
		TaskMutations:      s.lastTurn.TaskMutations,
		CodeEditToolCalls:  s.lastTurn.CodeEditToolCalls,
	}
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

// runPostStopValidation fires PostStopValidation hooks when the agent
// stops naturally and there are unverified code edits since the last
// successful hook run. Runs asynchronously to avoid blocking the agent-end path.
func (p *sessionRuntimePolicy) runPostStopValidation() {
	s := p.session
	if s.hookRunner == nil {
		return
	}
	s.mu.Lock()
	summary := s.lastRunSummary
	seq := s.dirtySeq
	gen := s.generation
	s.mu.Unlock()

	if summary == nil || summary.EndReason != agentcore.EndReasonStop {
		return
	}
	if seq == 0 {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		failOutput := s.hookRunner.RunPostStopValidation(ctx)

		s.mu.Lock()
		if s.generation != gen {
			s.mu.Unlock()
			return // session switched; discard stale result
		}
		if failOutput == "" {
			// Only clear dirty if no new mutations happened while the hook was running.
			if s.dirtySeq == seq {
				s.dirtySeq = 0
			}
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		s.continueWithRuntimeReminder(
			"post_stop_validation",
			ReminderPostStopValidation,
			fmt.Sprintf(
				"<system-reminder>\nThe PostStopValidation hook failed. Fix the problem based on the following output:\n%s\n</system-reminder>",
				failOutput,
			),
		)
	}()
}
