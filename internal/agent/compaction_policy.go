package agent

import (
	"context"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/memory"
)

var compactableTools = map[string]bool{
	"read":       true,
	"bash":       true,
	"grep":       true,
	"glob":       true,
	"ls":         true,
	"web_search": true,
	"web_fetch":  true,
}

func CodebotToolClassifier(name string) bool {
	if strings.HasPrefix(name, "mcp__") {
		return false
	}
	return compactableTools[name]
}

func codebotToolClassifier(name string) bool {
	return CodebotToolClassifier(name)
}

func (s *Session) PostCompactRecoveryHook() memory.PostCompactHook {
	return s.postCompactRecoveryMessages
}

func (s *Session) HandleProjectedCompaction(info memory.ChangeInfo) {
	s.handleAutomaticCompaction(info)
}

func (s *Session) HandleOverflowRecovery(info memory.ChangeInfo) {
	s.handleAutomaticCompaction(info)
}

func (s *Session) handleAutomaticCompaction(info memory.ChangeInfo) {
	if !info.Changed {
		return
	}
	kind := compactionKindForStrategy(info.Strategy)
	s.recordCompactionAttempt(kind)
	s.emit(SessionEvent{
		Type:               SEAutoCompactionStart,
		CompactionReason:   info.Reason,
		CompactionKind:     kind,
		CompactionStrategy: info.Strategy,
	})
	s.recordCompactionResult(kind, true, info.TokensBefore, info.TokensAfter)
	compactedCount, keptCount, splitTurn := 0, 0, false
	if info.Info != nil {
		compactedCount = info.Info.CompactedCount
		keptCount = info.Info.KeptCount
		splitTurn = info.Info.IsSplitTurn
	}
	s.recordCompactionSnapshot(kind, info.Strategy, info.Reason, true, info.TokensBefore, info.TokensAfter, compactedCount, keptCount, splitTurn)
	s.emit(SessionEvent{
		Type:               SEAutoCompactionEnd,
		CompactionReason:   info.Reason,
		CompactionKind:     kind,
		CompactionStrategy: info.Strategy,
		CompactionChanged:  true,
		TokensBefore:       info.TokensBefore,
		TokensAfter:        info.TokensAfter,
		CompactedCount:     compactedCount,
		KeptCount:          keptCount,
		SplitTurn:          splitTurn,
	})
}

func compactionKindForStrategy(name string) CompactionKind {
	switch name {
	case "tool_result_microcompact":
		return CompactionKindMicro
	case "light_trim":
		return CompactionKindTrim
	default:
		return CompactionKindFull
	}
}

func (s *Session) postCompactRecoveryMessages(ctx context.Context, info memory.CompactionInfo, kept []agentcore.AgentMessage) ([]agentcore.AgentMessage, error) {
	_ = ctx
	_ = info
	_ = kept

	var out []agentcore.AgentMessage
	if reminder := s.invokedSkillReminderMessage(); reminder != nil {
		out = append(out, *reminder)
	}

	s.mu.Lock()
	preamble := s.deferredToolsPreamble
	staticReminders := append([]string(nil), s.staticReminders...)
	s.mu.Unlock()

	if preamble != "" {
		out = append(out, injectedUserMsg(preamble))
	}
	for _, text := range staticReminders {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, injectedUserMsg(text))
	}
	return out, nil
}
