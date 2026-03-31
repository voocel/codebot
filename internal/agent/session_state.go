package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
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
	if s.runtime != nil {
		s.runtime.handleEvent(ev)
	}

	if ev.Type == agentcore.EventMessageEnd {
		if msg, ok := ev.Message.(agentcore.Message); ok {
			s.persistence.handleMessageEnd(msg)
			if isRuntimeReminderMessage(msg) {
				s.mu.Lock()
				s.pendingReminderContinue = false
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
		if s.runtime != nil && s.runtime.continuePendingReminder() {
			return
		}
		if s.runtime != nil {
			s.runtime.afterAgentEnd()
		}
		if s.hookRunner != nil {
			s.hookRunner.RunNotification(context.Background(), "agent response complete")
		}
	}

	s.emit(SessionEvent{
		Type:       SEAgentEvent,
		AgentEvent: &ev,
	})
}

func (s *Session) emit(ev SessionEvent) {
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

func (s *Session) emitContinueError(err error) {
	s.emit(SessionEvent{
		Type:  SEError,
		Error: fmt.Errorf("overflow auto-continue: %w", err),
	})
	s.emit(SessionEvent{
		Type:       SEAgentEvent,
		AgentEvent: &agentcore.Event{Type: agentcore.EventAgentEnd},
	})
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
		p.tryAutoName()
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
		c.session.mu.Lock()
		c.session.overflowDetected = true
		c.session.mu.Unlock()
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
	overflow := c.session.overflowDetected
	c.session.retryAttempt = 0
	c.session.overflowDetected = false
	c.session.mu.Unlock()

	if retryAttempt > 0 {
		c.session.emit(SessionEvent{
			Type:         SEAutoRetryEnd,
			RetryAttempt: retryAttempt,
			RetrySuccess: true,
		})
	}

	if overflow {
		if result, err := c.compactWithReason("overflow"); err == nil && result.Changed {
			go func() {
				if err := c.session.agent.Continue(); err != nil {
					c.session.emitContinueError(err)
				}
			}()
			return true
		}
	} else {
		c.checkAutoCompaction()
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
