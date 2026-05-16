package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/storage"
)

type sessionPersistence struct {
	session *Session
}

type sessionContextController struct {
	session *Session
}

func newSessionPersistence(session *Session) *sessionPersistence {
	return &sessionPersistence{session: session}
}

func newSessionContextController(session *Session) *sessionContextController {
	return &sessionContextController{session: session}
}

func (s *Session) Subscribe(fn func(SessionEvent)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
	idx := len(s.listeners) - 1
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.listeners[idx] = nil
	}
}

func (s *Session) handleAgentEvent(ev agentcore.Event) {
	s.runtime.handleEvent(ev)

	if ev.Type == agentcore.EventMessageStart {
		if msg, ok := ev.Message.(agentcore.Message); ok && msg.Role == agentcore.RoleAssistant {
			s.mu.Lock()
			s.lastAssistantStart = time.Now()
			s.mu.Unlock()
		}
	}

	if ev.Type == agentcore.EventMessageEnd {
		if msg, ok := ev.Message.(agentcore.Message); ok {
			s.persistence.handleMessageEnd(msg)
			if isRuntimeReminderMessage(msg) {
				s.mu.Lock()
				s.reminders.pendingContinue = false
				s.mu.Unlock()
			}
			if msg.Role == agentcore.RoleAssistant {
				s.recordAssistantTurnMessage(msg)
			}
		}
	}

	if ev.Type == agentcore.EventRetry && ev.RetryInfo != nil {
		s.context.handleRetry(ev.RetryInfo)
	}

	if ev.Type == agentcore.EventAgentEnd {
		if ev.Summary != nil {
			s.mu.Lock()
			summary := *ev.Summary
			s.lastRunSummary = &summary
			s.mu.Unlock()
		}
		s.persistence.flushPendingMessages()
		if s.context.handleAgentEnd() {
			return
		}
		s.finalizeTurnOutcome()
		if s.runtime.continuePendingReminder() {
			return
		}
		s.runtime.afterAgentEnd()
		if s.hookRunner != nil {
			s.hookRunner.RunNotification(context.Background(), "agent response complete")
		}
		s.clearSkillDelta()
	}

	s.emit(SessionEvent{
		Type:       SEAgentEvent,
		AgentEvent: &ev,
	})
}

func (s *Session) emit(ev SessionEvent) {
	switch ev.Type {
	case SEError:
		s.recordErrorDiagnostic(ev.Error)
	case SEAgentEvent:
		if ev.AgentEvent != nil && ev.AgentEvent.Type == agentcore.EventError {
			s.recordErrorDiagnostic(ev.AgentEvent.Err)
		}
	case SEAutoCompactionEnd:
		if ev.CompactionChanged {
			// A compaction rewrites the prompt prefix: the next turn's
			// cache_read drop is expected, not a bug. Invalidate the cache
			// baseline so detectCacheBreak does not flag it as a break.
			s.mu.Lock()
			s.cacheSnap.CacheReadTokens = 0
			s.cacheSnap.Valid = false
			s.mu.Unlock()
		}
	}

	s.mu.Lock()
	listeners := make([]func(SessionEvent), len(s.listeners))
	copy(listeners, s.listeners)
	s.mu.Unlock()

	for _, fn := range listeners {
		if fn != nil {
			fn(ev)
		}
	}
}

func (s *Session) ApplySkillInvocation(result *skill.InvocationResult) error {
	if result == nil {
		return nil
	}
	s.recordInvokedSkill(result.Spec.Name, result.PromptText, result.Delta.Paths)
	if result.Mode == skill.ModeFork {
		return nil
	}
	return s.ApplySkillDelta(result.Spec.Name, result.Delta)
}

func (s *Session) ApplySkillDelta(name string, delta skill.Delta) error {
	if err := s.applyTemporarySkillModel(delta.ModelOverride); err != nil {
		return err
	}
	s.applyTemporarySkillThinking(delta.Effort)

	if s.skillAllowsSetter != nil {
		s.skillAllowsSetter(delta.AllowedTools)
	}
	s.applySkillPathHints(name, delta.Paths)
	return nil
}

func (s *Session) clearSkillDelta() {
	if s.skillAllowsSetter != nil {
		s.skillAllowsSetter(nil)
	}
	s.clearTemporarySkillOverrides()
}

func (s *Session) recordInvokedSkill(name, promptText string, paths []string) {
	name = strings.TrimSpace(name)
	promptText = strings.TrimSpace(promptText)
	if name == "" || promptText == "" {
		return
	}
	usageName := skill.NormalizeName(name)
	if usageName == "" {
		usageName = name
	}

	snapshot := invokedSkillSnapshot{
		Name:       name,
		PromptText: truncateRunes(promptText, 2400),
		Paths:      append([]string(nil), paths...),
		Timestamp:  time.Now(),
	}

	s.mu.Lock()
	if s.skillRuntime.invocationCount == nil {
		s.skillRuntime.invocationCount = make(map[string]int)
	}
	s.skillRuntime.invocationCount[usageName]++
	skillList := append(s.skillRuntime.invoked, snapshot)
	if len(skillList) > 4 {
		skillList = append([]invokedSkillSnapshot(nil), skillList[len(skillList)-4:]...)
	}
	s.skillRuntime.invoked = skillList
	s.mu.Unlock()

	if s.skillUsage != nil {
		if err := s.skillUsage.Record(usageName, time.Now()); err != nil {
			s.emit(SessionEvent{
				Type:  SEError,
				Error: fmt.Errorf("record skill usage: %w", err),
			})
		}
	}

	if s.prompts != nil {
		s.prompts.refreshSkillReminders()
	}
}

func invocationUsageScores(invocations map[string]int) map[string]float64 {
	if len(invocations) == 0 {
		return nil
	}
	scores := make(map[string]float64, len(invocations))
	for name, count := range invocations {
		name = skill.NormalizeName(name)
		if count <= 0 {
			continue
		}
		if name == "" {
			continue
		}
		scores[name] = float64(count)
	}
	if len(scores) == 0 {
		return nil
	}
	return scores
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max == 1 {
		return string(runes[:1])
	}
	return string(runes[:max-1]) + "…"
}

func (p *sessionPersistence) handleMessageEnd(msg agentcore.Message) {
	p.session.mu.Lock()
	lazy := p.session.lazyPersist
	p.session.mu.Unlock()

	if lazy && msg.Role == agentcore.RoleUser {
		p.session.mu.Lock()
		p.session.pendingUserMsg = append(p.session.pendingUserMsg, msg)
		p.session.mu.Unlock()
		return
	}

	if lazy && msg.Role == agentcore.RoleAssistant {
		p.flushPendingMessages()
	}
	p.persistMessage(msg)

	if msg.Role == agentcore.RoleAssistant {
		p.persistLLMCall(msg)
		p.tryAutoName()
		p.maybeExtractSessionMemory()
	}
}

// persistLLMCall writes a per-turn observability record for the just-finished
// assistant response. Non-fatal: logging failures are surfaced via SEError but
// never block the session. Skipped when usage is empty (e.g. recovered message).
func (p *sessionPersistence) persistLLMCall(msg agentcore.Message) {
	if msg.Usage == nil {
		return
	}
	u := msg.Usage
	if u.Input == 0 && u.Output == 0 && u.TotalTokens == 0 {
		return
	}

	p.session.mu.Lock()
	store := p.session.store
	start := p.session.lastAssistantStart
	p.session.lastAssistantStart = time.Time{}
	provider := p.session.provider
	model := p.session.modelName
	thinking := p.session.settings.ThinkingLevel
	prevSnap := p.session.cacheSnap
	currSnap := cacheSnapshot{
		FrozenSystemHash:  p.session.cacheSnap.FrozenSystemHash,
		DynamicSystemHash: p.session.cacheSnap.DynamicSystemHash,
		ToolsHash:         p.session.cacheSnap.ToolsHash,
		CacheReadTokens:   u.CacheRead,
		Valid:             true,
	}
	p.session.cacheSnap = currSnap
	p.session.mu.Unlock()

	if store == nil {
		return
	}

	var latencyMs int64
	if !start.IsZero() {
		latencyMs = time.Since(start).Milliseconds()
	}
	entry := storage.LLMCallEntry{
		Provider:            provider,
		Model:               model,
		InputTokens:         u.Input,
		OutputTokens:        u.Output,
		CacheReadTokens:     u.CacheRead,
		CacheCreationTokens: u.CacheWrite,
		TotalTokens:         u.TotalTokens,
		LatencyMs:           latencyMs,
		StopReason:          string(msg.StopReason),
		ThinkingLevel:       thinking,
		CacheBreak:          detectCacheBreak(prevSnap, currSnap),
	}
	if err := store.AppendLLMCall(entry); err != nil {
		p.session.emit(SessionEvent{
			Type:  SEError,
			Error: fmt.Errorf("persist llm_call: %w", err),
		})
	}
}

func (p *sessionPersistence) persistMessage(msg agentcore.Message) {
	p.session.mu.Lock()
	store := p.session.store
	p.session.mu.Unlock()
	if store != nil {
		if err := store.AppendMessage(msg); err != nil {
			detail := err.Error()
			for _, tc := range msg.ToolCalls() {
				if !json.Valid(tc.Args) {
					detail = fmt.Sprintf("%s [invalid args in %s(%s): %s]",
						detail, tc.Name, tc.ID, truncateBytes(tc.Args, 200))
				}
			}
			p.session.emit(SessionEvent{
				Type:  SEError,
				Error: fmt.Errorf("persist message: %s", detail),
			})
		}
	}
}

func (p *sessionPersistence) flushPendingMessages() {
	p.session.mu.Lock()
	pending := p.session.pendingUserMsg
	p.session.pendingUserMsg = nil
	p.session.mu.Unlock()

	for _, msg := range pending {
		p.persistMessage(msg)
	}
}

func (p *sessionPersistence) tryAutoName() {
	p.session.mu.Lock()
	if p.session.autoNamed || p.session.store == nil {
		p.session.mu.Unlock()
		return
	}
	store := p.session.store
	p.session.autoNamed = true
	p.session.mu.Unlock()

	if h := store.Header(); h.Name != "" {
		return
	}

	var name string
	for _, m := range p.session.agent.Messages() {
		msg, ok := m.(agentcore.Message)
		if !ok || msg.Role != agentcore.RoleUser {
			continue
		}
		if msg.Metadata["injected"] == true {
			continue
		}
		text := lastTextBlock(msg)
		if text == "" {
			continue
		}
		if len([]rune(text)) > 50 {
			text = string([]rune(text)[:50])
		}
		name = strings.ReplaceAll(text, "\n", " ")
		break
	}

	if name != "" {
		go func() { _ = store.SetName(name) }()
	}
}

func (c *sessionContextController) handleRetry(info *agentcore.RetryInfo) {
	if agentcore.IsContextOverflow(info.Err) {
		return
	}

	c.session.mu.Lock()
	c.session.retryAttempt = info.Attempt
	c.session.mu.Unlock()
	c.session.emit(SessionEvent{
		Type:         SEAutoRetryStart,
		RetryAttempt: info.Attempt,
		RetryMax:     info.MaxRetries,
		RetryDelay:   info.Delay,
	})
}

func (c *sessionContextController) handleAgentEnd() bool {
	c.session.mu.Lock()
	retryAttempt := c.session.retryAttempt
	c.session.retryAttempt = 0
	c.session.mu.Unlock()

	if retryAttempt > 0 {
		c.session.emit(SessionEvent{
			Type:         SEAutoRetryEnd,
			RetryAttempt: retryAttempt,
			RetrySuccess: true,
		})
	}

	return false
}

// lastTextBlock returns the text of the last ContentText block in msg.
// In buildUserMessage, reminder blocks are prepended and the user's actual
// input is always the final text block, so this extracts the real user text.
func lastTextBlock(msg agentcore.Message) string {
	var last string
	for _, b := range msg.Content {
		if b.Type == agentcore.ContentText && b.Text != "" {
			last = b.Text
		}
	}
	return last
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

func isRuntimeReminderMessage(msg agentcore.Message) bool {
	if msg.Role != agentcore.RoleUser || msg.Metadata["injected"] != true {
		return false
	}
	for _, block := range msg.Content {
		if block.Type == agentcore.ContentText && strings.Contains(block.Text, "<system-reminder>") {
			return true
		}
	}
	return false
}
