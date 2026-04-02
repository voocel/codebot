package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

type contextPressureStage int

const (
	contextStageNormal contextPressureStage = iota
	contextStageWarning
	contextStageTrim
	contextStagePrune
	contextStageCompact
)

const (
	microcompactThreshold    = 40000
	microcompactMinSaving    = 20000
	largeTextThreshold       = 4000
	microcompactPreserveHead = 1500
	microcompactPreserveTail = 1500
	microcompactKeepRecent   = 3

	trimStagePercent     = 80
	pruneStagePercent    = 90
	compactStagePercent  = 98
	trimPreserveHead     = 1200
	trimPreserveTail     = 800
	trimKeepRecent       = 4
	prunePreserveHead    = 400
	prunePreserveTail    = 200
	pruneKeepRecent      = 2
	pruneTextThreshold   = 1500
	prunePlaceholderText = "[Earlier long output omitted to save context. Re-run the relevant step or inspect session history if the exact content is needed.]"
)

func (s *Session) Compact() (CompactionResult, error) {
	return s.context.compactWithReason("manual")
}

func stageForPercent(percent float64) contextPressureStage {
	switch {
	case percent >= compactStagePercent:
		return contextStageCompact
	case percent >= pruneStagePercent:
		return contextStagePrune
	case percent >= trimStagePercent:
		return contextStageTrim
	case percent >= 70:
		return contextStageWarning
	default:
		return contextStageNormal
	}
}

func (c *sessionContextController) microcompact() bool {
	msgs := c.session.agent.Messages()
	totalTokens := memory.EstimateTotal(msgs)
	if totalTokens < microcompactThreshold {
		return false
	}
	before := totalTokens
	after := totalTokens
	changed := false
	c.session.recordCompactionAttempt(CompactionKindMicro)
	defer func() {
		c.session.recordCompactionResult(CompactionKindMicro, changed, before, after)
		c.session.recordCompactionSnapshot(CompactionKindMicro, "threshold", changed, before, after)
	}()

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
	newMsgs = c.session.injectInvokedSkillContext(newMsgs)

	if err := c.session.agent.SetMessages(newMsgs); err != nil {
		return false
	}
	after = memory.EstimateTotal(newMsgs)
	changed = true
	return true
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
		c.session.recordCompactionSnapshot(CompactionKindFull, reason, result.Changed, result.TokensBefore, result.TokensAfter)
		c.session.emit(SessionEvent{
			Type:              SEAutoCompactionEnd,
			CompactionReason:  reason,
			CompactionKind:    CompactionKindFull,
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
	compacted = c.session.injectInvokedSkillContext(compacted)

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

func (c *sessionContextController) promptCompact() bool {
	cu := c.session.agent.ContextUsage()
	stage := contextStageNormal
	if cu != nil {
		stage = stageForPercent(cu.Percent)
	}

	switch stage {
	case contextStageTrim, contextStagePrune:
		result, err := c.lightweightCompact("threshold", stage)
		if err != nil {
			c.session.emit(SessionEvent{
				Type:  SEError,
				Error: fmt.Errorf("prompt compaction failed: %w", err),
			})
			return false
		}
		return result.Changed
	case contextStageCompact:
		result, err := c.compactWithReason("threshold")
		if err != nil {
			c.session.emit(SessionEvent{
				Type:  SEError,
				Error: fmt.Errorf("prompt compaction failed: %w", err),
			})
			return false
		}
		return result.Changed
	default:
		return c.microcompact()
	}
}

func (c *sessionContextController) lightweightCompact(reason string, stage contextPressureStage) (result CompactionResult, err error) {
	result = CompactionResult{Reason: reason}
	kind := compactionKindForStage(stage)
	c.session.recordCompactionAttempt(kind)
	c.session.emit(SessionEvent{Type: SEAutoCompactionStart, CompactionReason: reason, CompactionKind: kind})
	defer func() {
		if err != nil {
			return
		}
		c.session.recordCompactionResult(kind, result.Changed, result.TokensBefore, result.TokensAfter)
		c.session.recordCompactionSnapshot(kind, reason, result.Changed, result.TokensBefore, result.TokensAfter)
		c.session.emit(SessionEvent{
			Type:              SEAutoCompactionEnd,
			CompactionReason:  reason,
			CompactionKind:    kind,
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

	newMsgs, changed := applyStageCompactionToMessages(msgs, stage)
	if !changed {
		result.TokensAfter = tokensBefore
		return result, nil
	}
	newMsgs = c.session.injectInvokedSkillContext(newMsgs)

	tokensAfter := memory.EstimateTotal(newMsgs)
	if tokensAfter >= tokensBefore {
		result.TokensAfter = tokensBefore
		return result, nil
	}

	if err := c.session.agent.SetMessages(newMsgs); err != nil {
		return result, fmt.Errorf("set compacted messages: %w", err)
	}
	result.Changed = true
	result.TokensAfter = tokensAfter
	return result, nil
}

func compactionKindForStage(stage contextPressureStage) CompactionKind {
	switch stage {
	case contextStagePrune:
		return CompactionKindPrune
	case contextStageTrim:
		return CompactionKindTrim
	default:
		return CompactionKindFull
	}
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
	if cu == nil {
		return
	}

	stage := stageForPercent(cu.Percent)
	var err error
	switch stage {
	case contextStageTrim, contextStagePrune:
		_, err = c.lightweightCompact("threshold", stage)
	case contextStageCompact:
		_, err = c.compactWithReason("threshold")
	default:
		return
	}
	if err != nil {
		c.session.emit(SessionEvent{
			Type:  SEError,
			Error: fmt.Errorf("auto compact failed: %w", err),
		})
	}
}

func applyStageCompactionToMessages(msgs []agentcore.AgentMessage, stage contextPressureStage) ([]agentcore.AgentMessage, bool) {
	switch stage {
	case contextStageTrim:
		return compactMessagesByPolicy(msgs, trimKeepRecent, largeTextThreshold, trimPreserveHead, trimPreserveTail, false)
	case contextStagePrune:
		return compactMessagesByPolicy(msgs, pruneKeepRecent, pruneTextThreshold, prunePreserveHead, prunePreserveTail, true)
	default:
		return msgs, false
	}
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
		if _, ok := m.(memory.CompactionSummary); ok {
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

func compactMessagesByPolicy(msgs []agentcore.AgentMessage, keepRecent, threshold, preserveHead, preserveTail int, mask bool) ([]agentcore.AgentMessage, bool) {
	if len(msgs) == 0 {
		return msgs, false
	}

	candidates := eligibleMessageIndexes(msgs, threshold, keepRecent)
	if len(candidates) == 0 {
		return msgs, false
	}

	eligible := make(map[int]struct{}, len(candidates))
	for _, idx := range candidates {
		eligible[idx] = struct{}{}
	}

	newMsgs := make([]agentcore.AgentMessage, len(msgs))
	changed := false
	for i, m := range msgs {
		if _, ok := eligible[i]; ok {
			if msg, ok := m.(agentcore.Message); ok {
				var next agentcore.Message
				if mask {
					next = maskMessageText(msg, threshold, preserveHead, preserveTail)
				} else {
					next = truncateMessageTextWithConfig(msg, threshold, preserveHead, preserveTail)
				}
				if !messagesEqual(msg, next) {
					newMsgs[i] = next
					changed = true
					continue
				}
			}
		}
		newMsgs[i] = m
	}
	return newMsgs, changed
}

func eligibleMessageIndexes(msgs []agentcore.AgentMessage, threshold, keepRecent int) []int {
	lastEligible := len(msgs) - keepRecent
	if lastEligible <= 0 {
		return nil
	}

	var indexes []int
	for i, m := range msgs[:lastEligible] {
		msg, ok := m.(agentcore.Message)
		if !ok {
			continue
		}
		if messageHasLargeText(msg, threshold) {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func messageHasLargeText(msg agentcore.Message, threshold int) bool {
	for _, b := range msg.Content {
		if b.Type == agentcore.ContentText && len([]rune(b.Text)) > threshold {
			return true
		}
	}
	return false
}

func truncateMessageText(msg agentcore.Message) agentcore.Message {
	return truncateMessageTextWithConfig(msg, largeTextThreshold, microcompactPreserveHead, microcompactPreserveTail)
}

func truncateMessageTextWithConfig(msg agentcore.Message, threshold, preserveHead, preserveTail int) agentcore.Message {
	newContent := make([]agentcore.ContentBlock, len(msg.Content))
	for i, b := range msg.Content {
		if b.Type == agentcore.ContentText && len([]rune(b.Text)) > threshold {
			runes := []rune(b.Text)
			headCount := min(preserveHead, len(runes))
			tailCount := min(preserveTail, len(runes)-headCount)
			head := string(runes[:headCount])
			tail := string(runes[len(runes)-tailCount:])
			trimmed := len(runes) - headCount - tailCount
			if trimmed < 0 {
				trimmed = 0
			}
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

func maskMessageText(msg agentcore.Message, threshold, preserveHead, preserveTail int) agentcore.Message {
	newContent := make([]agentcore.ContentBlock, len(msg.Content))
	for i, b := range msg.Content {
		if b.Type == agentcore.ContentText && len([]rune(b.Text)) > threshold {
			runes := []rune(b.Text)
			headCount := min(preserveHead, len(runes))
			tailCount := min(preserveTail, len(runes)-headCount)
			head := string(runes[:headCount])
			tail := string(runes[len(runes)-tailCount:])
			newContent[i] = agentcore.ContentBlock{
				Type: agentcore.ContentText,
				Text: fmt.Sprintf("%s\n%s\n%s", head, prunePlaceholderText, tail),
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

func messagesEqual(a, b agentcore.Message) bool {
	if len(a.Content) != len(b.Content) {
		return false
	}
	for i := range a.Content {
		if a.Content[i] != b.Content[i] {
			return false
		}
	}
	return a.Role == b.Role &&
		a.StopReason == b.StopReason &&
		a.Timestamp.Equal(b.Timestamp)
}
