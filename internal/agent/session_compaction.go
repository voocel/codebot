package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/memory"
)

type CompactionResult struct {
	Changed      bool
	TokensBefore int
	TokensAfter  int
	Reason       string
}

const (
	microcompactThreshold    = 40000
	microcompactMinSaving    = 20000
	largeTextThreshold       = 4000
	microcompactPreserveHead = 1500
	microcompactPreserveTail = 1500
	microcompactKeepRecent   = 3
)

func (s *Session) Compact() (CompactionResult, error) {
	return s.context.compactWithReason("manual")
}

func (c *sessionContextController) microcompact() bool {
	msgs := c.session.agent.Messages()
	totalTokens := memory.EstimateTotal(msgs)
	if totalTokens < microcompactThreshold {
		return false
	}

	type candidate struct {
		idx     int
		savings int
	}
	var candidates []candidate
	for i, m := range msgs {
		msg, ok := m.(agentcore.Message)
		if !ok {
			continue
		}
		for _, b := range msg.Content {
			if b.Type == agentcore.ContentText && len([]rune(b.Text)) > largeTextThreshold {
				saved := len(b.Text) - microcompactPreserveHead*3
				if saved > 0 {
					candidates = append(candidates, candidate{idx: i, savings: saved / 4})
				}
				break
			}
		}
	}

	if len(candidates) == 0 {
		return false
	}

	protectCount := min(microcompactKeepRecent, len(candidates))
	eligible := candidates[:len(candidates)-protectCount]
	if len(eligible) == 0 {
		return false
	}

	var totalSavings int
	for _, candidate := range eligible {
		totalSavings += candidate.savings
	}
	if totalSavings < microcompactMinSaving {
		return false
	}

	eligibleSet := make(map[int]struct{}, len(eligible))
	for _, candidate := range eligible {
		eligibleSet[candidate.idx] = struct{}{}
	}

	newMsgs := make([]agentcore.AgentMessage, len(msgs))
	for i, m := range msgs {
		if _, ok := eligibleSet[i]; ok {
			if msg, ok := m.(agentcore.Message); ok {
				newMsgs[i] = truncateMessageText(msg)
				continue
			}
		}
		newMsgs[i] = m
	}

	if err := c.session.agent.SetMessages(newMsgs); err != nil {
		return false
	}
	return true
}

func (c *sessionContextController) compactWithReason(reason string) (result CompactionResult, err error) {
	result = CompactionResult{Reason: reason}
	c.session.emit(SessionEvent{Type: SEAutoCompactionStart, CompactionReason: reason})
	defer func() {
		if err != nil {
			return
		}
		c.session.emit(SessionEvent{
			Type:              SEAutoCompactionEnd,
			CompactionReason:  reason,
			CompactionChanged: result.Changed,
			TokensBefore:      result.TokensBefore,
			TokensAfter:       result.TokensAfter,
		})
	}()

	msgs := c.session.agent.Messages()
	if len(msgs) == 0 {
		return result, nil
	}

	tokensBefore := memory.EstimateTotal(msgs)
	result.TokensBefore = tokensBefore

	c.session.mu.Lock()
	prov := c.session.provider
	model := c.session.modelName
	ctxWindow := c.session.settings.ContextWindow
	store := c.session.store
	c.session.mu.Unlock()

	apiKey, baseURL := c.session.resolveCredentials(prov)
	compactModel, err := c.session.createModel(prov, model, apiKey, baseURL)
	if err != nil {
		return result, fmt.Errorf("create compaction model: %w", err)
	}

	transform := memory.NewCompaction(memory.CompactionConfig{
		Model:         compactModel,
		ContextWindow: ctxWindow,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	compacted, err := transform(ctx, msgs)
	if err != nil {
		return result, fmt.Errorf("compact context: %w", err)
	}

	tokensAfter := memory.EstimateTotal(compacted)
	if tokensAfter >= tokensBefore {
		result.TokensAfter = tokensBefore
		return result, nil
	}

	summary, keptRaw := extractCompactionPayload(compacted)
	if summary == "" {
		result.TokensAfter = tokensBefore
		return result, nil
	}

	if store != nil {
		if err := store.AppendCompaction(summary, keptRaw); err != nil {
			return result, fmt.Errorf("append compaction entry: %w", err)
		}
	}

	if err := c.session.agent.SetMessages(compacted); err != nil {
		return result, fmt.Errorf("set compacted messages: %w", err)
	}
	result.Changed = true
	result.TokensAfter = tokensAfter
	return result, nil
}

func extractCompactionPayload(msgs []agentcore.AgentMessage) (string, []json.RawMessage) {
	var summary string
	start := -1
	for i, m := range msgs {
		cs, ok := m.(memory.CompactionSummary)
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

func (c *sessionContextController) checkAutoCompaction() {
	if !c.session.settings.AutoCompaction {
		return
	}
	cu := c.session.agent.ContextUsage()
	if cu == nil || cu.Percent < 80 {
		return
	}
	if _, err := c.compactWithReason("threshold"); err != nil {
		c.session.emit(SessionEvent{
			Type:  SEError,
			Error: fmt.Errorf("auto compact failed: %w", err),
		})
	}
}

func truncateMessageText(msg agentcore.Message) agentcore.Message {
	newContent := make([]agentcore.ContentBlock, len(msg.Content))
	for i, b := range msg.Content {
		if b.Type == agentcore.ContentText && len([]rune(b.Text)) > largeTextThreshold {
			runes := []rune(b.Text)
			head := string(runes[:microcompactPreserveHead])
			tail := string(runes[len(runes)-microcompactPreserveTail:])
			trimmed := len(runes) - microcompactPreserveHead - microcompactPreserveTail
			newContent[i] = agentcore.ContentBlock{
				Type: agentcore.ContentText,
				Text: fmt.Sprintf("%s\n[%d characters trimmed]\n%s", head, trimmed, tail),
			}
		} else {
			newContent[i] = b
		}
	}

	return agentcore.Message{
		Role:       msg.Role,
		Content:    newContent,
		StopReason: msg.StopReason,
		Usage:      msg.Usage,
		Metadata:   msg.Metadata,
		Timestamp:  msg.Timestamp,
	}
}
