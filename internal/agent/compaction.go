package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/memory"
)

// Compact performs manual compaction using the same strategy as agentcore memory compaction.
// It persists a compaction checkpoint to session log for deterministic restore.
func (s *Session) Compact() error {
	return s.compactWithReason("manual")
}

// compactWithReason performs compaction and emits events with the given reason
// ("overflow", "threshold", or "manual").
func (s *Session) compactWithReason(reason string) error {
	s.emit(SessionEvent{Type: SEAutoCompactionStart, CompactionReason: reason})
	defer s.emit(SessionEvent{Type: SEAutoCompactionEnd, CompactionReason: reason})

	msgs := s.agent.Messages()
	if len(msgs) == 0 {
		return nil
	}

	tokensBefore := memory.EstimateTotal(msgs)

	s.mu.Lock()
	prov := s.provider
	model := s.modelName
	apiKey := s.apiKey
	baseURL := s.baseURL
	ctxWindow := s.settings.ContextWindow
	store := s.store
	s.mu.Unlock()

	compactModel, err := s.createModel(prov, model, apiKey, baseURL)
	if err != nil {
		return fmt.Errorf("create compaction model: %w", err)
	}

	transform := memory.NewCompaction(memory.CompactionConfig{
		Model:         compactModel,
		ContextWindow: ctxWindow,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	compacted, err := transform(ctx, msgs)
	if err != nil {
		return fmt.Errorf("compact context: %w", err)
	}

	tokensAfter := memory.EstimateTotal(compacted)
	if tokensAfter >= tokensBefore {
		return nil
	}

	summary, keptRaw := extractCompactionPayload(compacted)
	if summary == "" {
		return nil
	}

	if store != nil {
		if err := store.AppendCompaction(summary, keptRaw); err != nil {
			return fmt.Errorf("append compaction entry: %w", err)
		}
	}

	if err := s.agent.SetMessages(compacted); err != nil {
		return fmt.Errorf("set compacted messages: %w", err)
	}
	return nil
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

// checkAutoCompaction checks if the context exceeds the threshold and triggers compaction.
func (s *Session) checkAutoCompaction() {
	if !s.settings.AutoCompaction {
		return
	}
	cu := s.agent.ContextUsage()
	if cu == nil || cu.Percent < 80 {
		return
	}
	if err := s.compactWithReason("threshold"); err != nil {
		s.emit(SessionEvent{
			Type:  SEError,
			Error: fmt.Errorf("auto compact failed: %w", err),
		})
	}
}
