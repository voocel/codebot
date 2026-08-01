package agent

import (
	"context"
	"errors"
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

func newSessionPersistence(session *Session) *sessionPersistence {
	return &sessionPersistence{session: session}
}

func (s *Session) Subscribe(fn func(SessionEvent)) func() {
	return s.events.subscribe(fn)
}

// handleAgentEvent runs on the event goroutine (G-event) — distinct from the
// agent-loop goroutine (G-loop) that runs the MessageCommitter and the
// ContextEngine hooks. On a chained continuation the next run's events arrive
// on a NEW G-event that overlaps the tail of this one (agentcore clears
// isRunning BEFORE dispatching EventAgentEnd), so "events are serial" only
// holds within a single run.
//
// Ordering constraints — do not reorder without re-deriving each:
//  1. runtime.handleEvent sees every event first: tool tracking must be
//     recorded before any branch below delivers reminders derived from it.
//  2. EventMessageStart marks lastAssistantStart; EventMessageEnd's
//     persistLLMCall consumes it — the pair yields latency_ms.
//  3. setRunSummary before the continuation branches — runPostStopValidation
//     (via afterAgentEnd) reads it.
//  4. flushPending before any continuation — a continuation's messages must
//     not reach the store ahead of the lazy queue.
//  5. endTelemetryRun before any continuation — the next run installs its
//     own span; ending late would close the wrong one.
//  6. finalizeTurnOutcome before the three continuation branches — the goal
//     no-activity check pairs lastReminder with the turn just finished.
//  7. clearSkillDelta after afterAgentEnd. Known quirk: a continuation
//     launched by afterAgentEnd still runs with the skill delta applied;
//     recorded here, semantics intentionally unchanged.
//  8. The continuation branches return early and skip the trailing emit:
//     frontends treat SEAgentEvent(EventAgentEnd) as "turn finished", and a
//     continuation means it is not.
//  9. emit takes no session lock (structural — see emit) and callbacks run
//     outside all locks.
//  10. Continuation launches need no session-side lock: a concurrent
//     Reset/SwitchSession holds the agent's run lifecycle and they fail fast
//     with ErrRunsHeld (handled silently). Never call Reset, SwitchSession,
//     ClearConversation, or agent.HoldRuns synchronously from this
//     goroutine — it is the one a hold waits on to drain.
func (s *Session) handleAgentEvent(ev agentcore.Event) {
	s.runtime.handleEvent(ev)

	if ev.Type == agentcore.EventError && isUsageLimitError(ev.Err) {
		if err := s.markGoalUsageLimited("provider usage limit reached"); err != nil {
			s.emit(SessionEvent{
				Type:  SEError,
				Error: fmt.Errorf("mark goal usage-limited: %w", err),
			})
		}
	}

	if ev.Type == agentcore.EventMessageStart {
		if msg, ok := ev.Message.(agentcore.Message); ok && msg.Role == agentcore.RoleAssistant {
			s.persist.markAssistantStart()
		}
	}

	if ev.Type == agentcore.EventMessageEnd {
		if msg, ok := ev.Message.(agentcore.Message); ok {
			s.persistence.handleMessageEnd(msg)
			if isRuntimeReminderMessage(msg) {
				s.reminders.clearPendingContinue()
			}
			if msg.Role == agentcore.RoleAssistant {
				s.recordAssistantTurnMessage(msg)
			}
		}
	}

	if ev.Type == agentcore.EventRetry && ev.RetryInfo != nil {
		s.handleRetryEvent(ev.RetryInfo)
	}

	if ev.Type == agentcore.EventAgentEnd {
		s.endTelemetryRun(ev.Err)
		if ev.Summary != nil {
			s.turn.setRunSummary(*ev.Summary)
		}
		if err := s.persist.flushPending(); err != nil {
			s.emit(SessionEvent{
				Type:  SEError,
				Error: err,
			})
		}
		s.handleRetryAgentEnd()
		s.finalizeTurnOutcome()
		if s.runtime.continuePendingReminder() {
			return
		}
		if s.continuePendingBackgroundResult() {
			return
		}
		s.runtime.afterAgentEnd()
		if s.deps.hookRunner != nil {
			s.deps.hookRunner.RunNotification(context.Background(), "agent response complete")
		}
		s.clearSkillDelta()
		if fn := s.hooks.getIdleHook(); fn != nil {
			fn()
		}
	}

	s.emit(SessionEvent{
		Type:       SEAgentEvent,
		AgentEvent: &ev,
	})
}

// emit never touches s.mu — metrics/cache/events all guard themselves, so it
// is safe to call from any goroutine, with or without the session lock held.
func (s *Session) emit(ev SessionEvent) {
	switch ev.Type {
	case SEError:
		s.metrics.recordError(ev.Error)
	case SEAgentEvent:
		if ev.AgentEvent != nil && ev.AgentEvent.Type == agentcore.EventError {
			s.metrics.recordError(ev.AgentEvent.Err)
		}
	case SEAutoCompactionEnd:
		if ev.CompactionChanged {
			// A compaction rewrites the prompt prefix, so the next turn's
			// cache_read drop is our own doing. The conversation continues,
			// so arm the attribution rather than dropping the baseline.
			s.cache.expectDrop()
		}
	}

	s.events.dispatch(ev)
}

func isUsageLimitError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, agentcore.ErrProviderQuota) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "usage_limit_reached") ||
		strings.Contains(msg, "usage limit") ||
		strings.Contains(msg, "insufficient_quota") ||
		strings.Contains(msg, "quota exceeded")
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
	if err := s.applyTemporarySkillThinking(delta.Effort); err != nil {
		return err
	}

	if fn := s.hooks.getSkillAllows(); fn != nil {
		fn(delta.AllowedTools)
	}
	s.applySkillPathHints(name, delta.Paths)
	return nil
}

func (s *Session) clearSkillDelta() {
	if fn := s.hooks.getSkillAllows(); fn != nil {
		fn(nil)
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

	// Log → usage tracker, each taking the prompt lock in turn (never nested)
	// — see promptState.recordInvoked. No prompt rebuild follows: usage only
	// ranks skills for RenderListing's budget selection, and the rendered
	// order is deliberately usage-independent so block 2 stays byte-stable.
	// The new score lands on the next explicit reload.
	if err := s.prompt.recordInvoked(snapshot, usageName); err != nil {
		s.emit(SessionEvent{
			Type:  SEError,
			Error: fmt.Errorf("record skill usage: %w", err),
		})
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

func (p *sessionPersistence) handleCommittedMessage(message agentcore.AgentMessage) error {
	msg, ok := message.(agentcore.Message)
	if !ok {
		return nil
	}

	lazy := p.session.deps.lazyPersist

	if lazy && msg.Role == agentcore.RoleUser {
		p.session.persist.queuePending(msg)
		return nil
	}

	if lazy && msg.Role == agentcore.RoleAssistant {
		if err := p.session.persist.flushPending(); err != nil {
			return err
		}
	}
	return p.session.persist.append(msg)
}

func (p *sessionPersistence) handleMessageEnd(msg agentcore.Message) {
	if msg.Role == agentcore.RoleAssistant {
		p.persistLLMCall(msg)
		p.tryAutoName()
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

	start := p.session.persist.takeAssistantStart()
	// Cross-group snapshot: the model may have switched between the LLM call
	// and this event. Observability-only record — a stale attribution in the
	// rare race is acceptable.
	provider, model, _ := p.session.model.current()
	thinking := p.session.model.currentSettings().ReasoningEffort

	prevSnap, currSnap := p.session.cache.observe(u.CacheRead, time.Now())

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
		ReasoningEffort:     thinking,
		CacheBreak:          detectCacheBreak(prevSnap, currSnap),
	}
	err := p.session.persist.withStore(func(store *storage.Store) error {
		return store.AppendLLMCall(entry)
	})
	if err != nil {
		p.session.emit(SessionEvent{
			Type:  SEError,
			Error: fmt.Errorf("persist llm_call: %w", err),
		})
	}
}

func (p *sessionPersistence) tryAutoName() {
	store := p.session.persist.claimAutoName()
	if store == nil {
		return
	}

	if h := store.Header(); h.Name != "" {
		return
	}

	var name string
	for _, m := range p.session.deps.agent.Messages() {
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

func (s *Session) handleRetryEvent(info *agentcore.RetryInfo) {
	if agentcore.IsContextOverflow(info.Err) {
		return
	}

	s.run.setRetryAttempt(info.Attempt)
	s.emit(SessionEvent{
		Type:         SEAutoRetryStart,
		RetryAttempt: info.Attempt,
		RetryMax:     info.MaxRetries,
		RetryDelay:   info.Delay,
	})
}

func (s *Session) handleRetryAgentEnd() {
	retryAttempt := s.run.takeRetryAttempt()

	if retryAttempt > 0 {
		s.emit(SessionEvent{
			Type:         SEAutoRetryEnd,
			RetryAttempt: retryAttempt,
			RetrySuccess: true,
		})
	}
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
