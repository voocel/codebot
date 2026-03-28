package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
)

const (
	maxRecentToolCalls        = 8
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
	CompletionClaim    bool
	ReadOnlyToolCalls  int
	WriteLikeToolCalls int
	TaskMutations      int
}

func newSessionRuntimePolicy(session *Session) *sessionRuntimePolicy {
	return &sessionRuntimePolicy{session: session}
}

func (p *sessionRuntimePolicy) beforePrompt() {
	p.session.context.promptCompact()
}

func (p *sessionRuntimePolicy) handleEvent(ev agentcore.Event) {
	switch ev.Type {
	case agentcore.EventToolExecStart:
		p.trackToolStart(ev)
	case agentcore.EventToolExecEnd:
		p.trackToolEnd(ev)
	}
}

func (p *sessionRuntimePolicy) afterAgentEnd() {
	if p.session.taskStore == nil {
		return
	}

	snapshot := p.session.taskStore.Snapshot()
	if snapshot.Total == 0 {
		return
	}
	if snapshot.Pending == 0 && snapshot.InProgress == 0 {
		return
	}
	if !p.session.lastTurnLooksComplete() {
		return
	}

	key := fmt.Sprintf("unfinished_tasks:%d:%d", snapshot.Pending, snapshot.InProgress)
	p.session.continueWithRuntimeReminder(
		key,
		ReminderUnfinishedTasks,
		fmt.Sprintf(
			"<system-reminder>\n当前任务列表中还有未完成项：%d 个 pending，%d 个 in_progress。下一轮回复前，先检查这些任务是否真的完成；如果没有完成，请继续执行或明确说明阻塞原因，不要直接宣称任务结束。\n</system-reminder>",
			snapshot.Pending,
			snapshot.InProgress,
		),
	)
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
	p.session.mu.Unlock()

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
			"<system-reminder>\n检测到你在重复调用同一个工具且参数基本相同。先总结当前已知信息、差距和下一步假设；避免在没有新信息时继续相同调用。\n</system-reminder>",
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
}

func (s *Session) recordAssistantTurnMessage(msg agentcore.Message) {
	text := strings.TrimSpace(msg.TextContent())
	s.mu.Lock()
	s.currentTurn.AssistantResponded = true
	s.currentTurn.CompletionClaim = looksLikeCompletionClaim(text)
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
	p.session.mu.Lock()
	pending := p.session.pendingReminderContinue
	p.session.pendingReminderContinue = false
	p.session.mu.Unlock()
	if !pending {
		return false
	}

	go func() {
		if err := p.session.agent.Continue(); err != nil {
			p.session.emit(SessionEvent{
				Type:  SEError,
				Error: fmt.Errorf("runtime reminder continue: %w", err),
			})
		}
	}()
	return true
}

func (s *Session) lastTurnLooksComplete() bool {
	s.mu.Lock()
	outcome := s.lastTurn
	s.mu.Unlock()
	return outcome.AssistantResponded && outcome.CompletionClaim
}

func (s *Session) LastTurnOutcome() TurnOutcomeSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return TurnOutcomeSnapshot{
		AssistantResponded: s.lastTurn.AssistantResponded,
		CompletionClaim:    s.lastTurn.CompletionClaim,
		ReadOnlyToolCalls:  s.lastTurn.ReadOnlyToolCalls,
		WriteLikeToolCalls: s.lastTurn.WriteLikeToolCalls,
		TaskMutations:      s.lastTurn.TaskMutations,
	}
}

func looksLikeCompletionClaim(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}

	negativeMarkers := []string{
		"未完成", "还没完成", "尚未完成", "没有完成", "需要继续", "仍需", "阻塞", "卡住",
		"无法完成", "不能完成", "需要更多信息", "需要进一步", "还在进行", "进行中",
		"not done", "not complete", "need more", "blocked", "in progress", "still need",
	}
	for _, marker := range negativeMarkers {
		if strings.Contains(lower, marker) {
			return false
		}
	}

	positiveMarkers := []string{
		"已完成", "已经完成", "完成了", "任务完成", "处理好了", "修复完成", "实现完成",
		"全部完成", "搞定了", "done", "completed", "fixed", "implemented", "all set", "resolved",
	}
	for _, marker := range positiveMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
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
