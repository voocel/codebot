package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
)

type CompactionResult struct {
	Changed        bool
	TokensBefore   int
	TokensAfter    int
	Reason         string
	Strategy       string
	CompactedCount int
	KeptCount      int
	SplitTurn      bool
}

func (s *Session) Compact() (CompactionResult, error) {
	return s.context.compactWithReason("manual")
}

func (c *sessionContextController) compactWithReason(reason string) (result CompactionResult, err error) {
	result = CompactionResult{Reason: reason}
	c.session.recordCompactionAttempt(CompactionKindFull)
	c.session.emit(SessionEvent{Type: SEAutoCompactionStart, CompactionReason: reason, CompactionKind: CompactionKindFull})
	defer func() {
		if err != nil {
			return
		}
		c.session.recordCompactionResult(CompactionKindFull, result.Changed, result.TokensBefore, result.TokensAfter)
		c.session.recordCompactionSnapshot(CompactionKindFull, result.Strategy, reason, result.Changed, result.TokensBefore, result.TokensAfter, result.CompactedCount, result.KeptCount, result.SplitTurn)
		c.session.emit(SessionEvent{
			Type:               SEAutoCompactionEnd,
			CompactionReason:   reason,
			CompactionKind:     CompactionKindFull,
			CompactionStrategy: result.Strategy,
			CompactionChanged:  result.Changed,
			TokensBefore:       result.TokensBefore,
			TokensAfter:        result.TokensAfter,
			CompactedCount:     result.CompactedCount,
			KeptCount:          result.KeptCount,
			SplitTurn:          result.SplitTurn,
		})
	}()

	msgs := c.session.agent.Messages()
	if len(msgs) == 0 {
		return result, nil
	}

	tokensBefore := agentctx.EstimateTotal(msgs)
	result.TokensBefore = tokensBefore

	c.session.mu.Lock()
	mgr := c.session.contextManager
	store := c.session.store
	c.session.mu.Unlock()

	if mgr == nil {
		return result, fmt.Errorf("compact context: no context manager configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	commit, err := mgr.Compact(ctx, msgs, agentcore.CompactReason(reason))
	if err != nil {
		return result, fmt.Errorf("compact context: %w", err)
	}
	if !commit.Changed || len(commit.Messages) == 0 {
		result.TokensAfter = tokensBefore
		return result, nil
	}
	result.Strategy = commit.Strategy
	result.CompactedCount = commit.CompactedCount
	result.KeptCount = commit.KeptCount
	result.SplitTurn = commit.SplitTurn
	commit.Messages = injectCommittedFileRestores(commit.Messages)
	tokensAfter := agentctx.EstimateTotal(commit.Messages)
	if commit.Usage != nil {
		cp := *commit.Usage
		cp.Tokens = tokensAfter
		if cp.ContextWindow > 0 {
			cp.Percent = float64(tokensAfter) / float64(cp.ContextWindow) * 100
		}
		commit.Usage = &cp
	}

	if tokensAfter >= tokensBefore {
		result.TokensAfter = tokensBefore
		return result, nil
	}
	if store != nil {
		if err := persistCompaction(store, commit.Messages); err != nil {
			return result, err
		}
	}
	if err := c.session.agent.SetMessages(commit.Messages); err != nil {
		return result, fmt.Errorf("set compacted messages: %w", err)
	}
	result.Changed = true
	result.TokensAfter = tokensAfter
	return result, nil
}

func injectCommittedFileRestores(msgs []agentcore.AgentMessage) []agentcore.AgentMessage {
	if len(msgs) == 0 {
		return msgs
	}
	summary, ok := msgs[0].(agentctx.ContextSummary)
	if !ok {
		return msgs
	}
	info := agentctx.SummaryInfo{
		ReadFiles:     append([]string(nil), summary.ReadFiles...),
		ModifiedFiles: append([]string(nil), summary.ModifiedFiles...),
	}
	restore := readRecentFiles(info, msgs[1:])
	if len(restore) == 0 {
		return msgs
	}
	out := make([]agentcore.AgentMessage, 0, len(msgs)+len(restore))
	out = append(out, msgs[0])
	out = append(out, restore...)
	out = append(out, msgs[1:]...)
	return out
}

func persistCompaction(store appendCompactionStore, msgs []agentcore.AgentMessage) error {
	if store == nil {
		return nil
	}
	summary, keptRaw := extractCompactionPayload(msgs)
	if summary == "" {
		return nil
	}
	if err := store.AppendCompaction(summary, keptRaw); err != nil {
		return fmt.Errorf("append compaction entry: %w", err)
	}
	return nil
}

type appendCompactionStore interface {
	AppendCompaction(summary string, kept []json.RawMessage) error
}

func extractCompactionPayload(msgs []agentcore.AgentMessage) (string, []json.RawMessage) {
	var summary string
	start := -1
	for i, m := range msgs {
		cs, ok := m.(agentctx.ContextSummary)
		if !ok {
			continue
		}
		summary = cs.Summary
		start = i + 1
		break
	}
	if summary == "" || start < 0 || start > len(msgs) {
		return "", nil
	}

	var keptRaw []json.RawMessage
	for _, m := range msgs[start:] {
		msg, ok := m.(agentcore.Message)
		if !ok {
			continue
		}
		data, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		keptRaw = append(keptRaw, data)
	}
	return summary, keptRaw
}

func (s *Session) injectInvokedSkillContext(msgs []agentcore.AgentMessage) []agentcore.AgentMessage {
	reminder := s.invokedSkillReminderMessage()
	if reminder == nil {
		return msgs
	}

	insertAt := 0
	var out []agentcore.AgentMessage
	for i, m := range msgs {
		if msg, ok := m.(agentcore.Message); ok && msg.Metadata["skill_preserve"] == true {
			continue
		}
		if _, ok := m.(agentctx.ContextSummary); ok {
			insertAt = i + 1
		}
		out = append(out, m)
	}

	if insertAt < 0 || insertAt > len(out) {
		insertAt = len(out)
	}
	withReminder := make([]agentcore.AgentMessage, 0, len(out)+1)
	withReminder = append(withReminder, out[:insertAt]...)
	withReminder = append(withReminder, *reminder)
	withReminder = append(withReminder, out[insertAt:]...)
	return withReminder
}

func (s *Session) invokedSkillReminderMessage() *agentcore.Message {
	s.mu.Lock()
	invoked := append([]invokedSkillSnapshot(nil), s.skillRuntime.invoked...)
	s.mu.Unlock()
	if len(invoked) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("<system-reminder>\n")
	sb.WriteString("Earlier in this conversation, the following skills were invoked. Preserve their intent unless the user explicitly redirects the task.\n")
	for _, item := range invoked {
		sb.WriteString("\n<invoked-skill name=\"")
		sb.WriteString(item.Name)
		sb.WriteString("\">\n")
		if len(item.Paths) > 0 {
			sb.WriteString("paths:\n")
			for _, p := range item.Paths {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				sb.WriteString("- ")
				sb.WriteString(p)
				sb.WriteString("\n")
			}
		}
		sb.WriteString(truncateRunes(strings.TrimSpace(item.PromptText), 1600))
		sb.WriteString("\n</invoked-skill>\n")
	}
	sb.WriteString("</system-reminder>")

	msg := injectedUserMsg(sb.String())
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]any)
	}
	msg.Metadata["skill_preserve"] = true
	return &msg
}
