package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
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
	if ev.Type == agentcore.EventMessageEnd {
		if msg, ok := ev.Message.(agentcore.Message); ok {
			s.persistence.handleMessageEnd(msg)
		}
	}

	if ev.Type == agentcore.EventRetry && ev.RetryInfo != nil {
		s.context.handleRetry(ev.RetryInfo)
	}

	if ev.Type == agentcore.EventAgentEnd {
		s.persistence.flushPendingMessages()
		if s.context.handleAgentEnd() {
			return
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
		c.session.overflowErr = info.Err
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
	overflowErr := c.session.overflowErr
	alreadyReduced := c.session.maxTokensReduced
	c.session.retryAttempt = 0
	c.session.overflowDetected = false
	c.session.overflowErr = nil
	c.session.mu.Unlock()

	if retryAttempt > 0 {
		c.session.emit(SessionEvent{
			Type:         SEAutoRetryEnd,
			RetryAttempt: retryAttempt,
			RetrySuccess: true,
		})
	}

	if overflow {
		if !alreadyReduced && c.tryReduceMaxTokens(overflowErr) {
			go func() {
				if err := c.session.agent.Continue(); err != nil {
					c.session.emitContinueError(err)
				}
			}()
			return true
		}
		c.restoreMaxTokens()
		if result, err := c.compactWithReason("overflow"); err == nil && result.Changed {
			go func() {
				if err := c.session.agent.Continue(); err != nil {
					c.session.emitContinueError(err)
				}
			}()
			return true
		}
	} else {
		c.restoreMaxTokens()
		c.checkAutoCompaction()
	}

	return false
}

func (c *sessionContextController) tryReduceMaxTokens(overflowErr error) bool {
	c.session.mu.Lock()
	chatModel := c.session.chatModel
	c.session.mu.Unlock()

	if overflowErr == nil || chatModel == nil {
		return false
	}

	inputTokens, contextLimit, ok := parseOverflowError(overflowErr)
	if !ok {
		return false
	}

	const minOutput = 3000
	const buffer = 1000
	available := contextLimit - inputTokens - buffer
	if available < minOutput {
		return false
	}

	type configGetter interface {
		GetConfig() *llm.GenerationConfig
	}
	cg, ok := chatModel.(configGetter)
	if !ok {
		return false
	}
	cfg := cg.GetConfig()
	if cfg == nil {
		return false
	}

	c.session.mu.Lock()
	if !c.session.maxTokensReduced {
		c.session.originalMaxTokens = cfg.MaxTokens
	}
	c.session.maxTokensReduced = true
	c.session.mu.Unlock()

	cfg.MaxTokens = available
	return true
}

func (c *sessionContextController) restoreMaxTokens() {
	c.session.mu.Lock()
	reduced := c.session.maxTokensReduced
	original := c.session.originalMaxTokens
	chatModel := c.session.chatModel
	c.session.maxTokensReduced = false
	c.session.mu.Unlock()

	if !reduced || chatModel == nil {
		return
	}

	type configGetter interface {
		GetConfig() *llm.GenerationConfig
	}
	if cg, ok := chatModel.(configGetter); ok {
		if cfg := cg.GetConfig(); cfg != nil {
			cfg.MaxTokens = original
		}
	}
}

func parseOverflowError(err error) (inputTokens, contextLimit int, ok bool) {
	if err == nil {
		return 0, 0, false
	}
	msg := err.Error()

	var nums []int
	i := 0
	for i < len(msg) {
		if msg[i] >= '0' && msg[i] <= '9' {
			j := i
			for j < len(msg) && msg[j] >= '0' && msg[j] <= '9' {
				j++
			}
			n := 0
			for _, c := range msg[i:j] {
				n = n*10 + int(c-'0')
			}
			if n >= 1000 && n <= 10_000_000 {
				nums = append(nums, n)
			}
			i = j
		} else {
			i++
		}
	}

	if len(nums) < 2 {
		return 0, 0, false
	}

	max1, max2 := 0, 0
	for _, n := range nums {
		if n > max1 {
			max2 = max1
			max1 = n
		} else if n > max2 {
			max2 = n
		}
	}
	if max1 <= 0 || max2 <= 0 || max1 == max2 {
		return 0, 0, false
	}

	return max1, max2, max1 > max2
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
