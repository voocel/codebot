package storage

import (
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
)

const interruptedToolResult = `"Tool execution was interrupted by a previous process termination. Its outcome and side effects are unknown; inspect the workspace or external state before retrying."`

// ContextSnapshot is the projected runtime state from a session log.
type ContextSnapshot struct {
	Messages        []agentcore.AgentMessage
	Provider        string
	Model           string
	ReasoningEffort string
	PlanSlug        string
	PlanPhase       string
	PlanPreMode     string
	Goal            GoalStateEntry
}

// BuildSnapshot reconstructs runtime state by walking the tree from the current leaf.
func (s *Store) BuildSnapshot() (ContextSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Reuse the open-time entries scan when available; otherwise re-read
	// the file. Consume on read so subsequent BuildSnapshot calls (or any
	// post-write call — appendEntry also clears it) get a fresh view.
	var entries map[string]Entry
	if s.openEntries != nil {
		entries = s.openEntries
		s.openEntries = nil
	} else {
		scanned, _, _, err := scanEntries(s.path)
		if err != nil {
			return ContextSnapshot{}, err
		}
		entries = scanned
	}

	// Walk from leaf to root.
	var chain []Entry
	visited := map[string]struct{}{}
	cur := s.leafID
	for cur != "" {
		if _, exists := visited[cur]; exists {
			return ContextSnapshot{}, fmt.Errorf("cycle detected in session chain at entry %s", cur)
		}
		visited[cur] = struct{}{}

		entry, ok := entries[cur]
		if !ok {
			return ContextSnapshot{}, fmt.Errorf("entry %s not found in session log", cur)
		}
		chain = append(chain, entry)

		if entry.ID == headerEntryID {
			break
		}
		cur = entry.ParentID
	}
	if len(chain) == 0 {
		return ContextSnapshot{}, fmt.Errorf("empty chain for leaf %s", s.leafID)
	}
	if chain[len(chain)-1].ID != headerEntryID {
		return ContextSnapshot{}, fmt.Errorf("chain does not reach session header")
	}

	// Reverse to chronological order.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	// Locate the last EntryCompaction so we can skip the JSON unmarshal of
	// pre-compaction EntryMessage entries — the compaction case below runs
	// msgs = nil, so any messages reduced before it are immediately discarded.
	// State-bearing entries (model / reasoning effort / plan / goal / session-info) are
	// kept because they may not have a post-compaction counterpart and
	// dropping them would silently lose model/plan/reasoning-effort selection.
	lastCompactionIdx := -1
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].Kind == EntryCompaction {
			lastCompactionIdx = i
			break
		}
	}

	// Build context from the chain.
	var msgs []agentcore.AgentMessage
	var summary *agentctx.ContextSummary
	lastProvider := ""
	lastModel := ""
	lastReasoningEffort := ""
	lastPlanSlug := ""
	lastPlanPhase := ""
	lastPlanPreMode := ""
	lastGoal := GoalStateEntry{Status: "off"}

	for i, entry := range chain {
		// Pre-compaction messages are superseded by the compaction summary;
		// skip their unmarshal to save work on long sessions with many
		// /compact rounds. Other entry kinds still flow through reduce.
		if lastCompactionIdx > 0 && i < lastCompactionIdx && entry.Kind == EntryMessage {
			continue
		}
		switch entry.Kind {
		case EntryMessage:
			var msg agentcore.Message
			if json.Unmarshal(entry.Data, &msg) == nil {
				msgs = append(msgs, msg)
			}
		case EntryModelChange:
			var mc ModelChange
			if json.Unmarshal(entry.Data, &mc) == nil {
				lastProvider = mc.Provider
				lastModel = mc.Model
			}
		case EntryReasoningEffortChange:
			var tc ReasoningEffortChange
			if json.Unmarshal(entry.Data, &tc) == nil {
				lastReasoningEffort = tc.Level
			}
		case EntryCompaction:
			var c Compaction
			if json.Unmarshal(entry.Data, &c) == nil {
				// Compaction replaces all prior messages with summary + kept messages.
				msgs = nil
				summary = nil
				if c.Summary != "" {
					// Preserve the checkpoint type for incremental compaction.
					summary = &agentctx.ContextSummary{Summary: c.Summary, Timestamp: entry.Timestamp}
				}
				for _, raw := range c.Messages {
					var msg agentcore.Message
					if json.Unmarshal(raw, &msg) == nil {
						msgs = append(msgs, msg)
					}
				}
			}
		case EntrySessionInfo:
			var info map[string]string
			if json.Unmarshal(entry.Data, &info) == nil {
				if name, ok := info["name"]; ok {
					s.header.Name = name
				}
			}
		case EntryPlanState:
			var ps PlanStateEntry
			if json.Unmarshal(entry.Data, &ps) == nil {
				lastPlanPhase = ps.Phase
				lastPlanSlug = ps.Slug
				lastPlanPreMode = ps.PreMode
			}
		case EntryGoalState:
			var gs GoalStateEntry
			if json.Unmarshal(entry.Data, &gs) == nil {
				lastGoal = gs
			}
		}
	}

	// CollectMessages drops custom checkpoints, so restore it after repair.
	restored := agentcore.ToAgentMessages(repairInterruptedToolCalls(agentcore.CollectMessages(msgs)))
	if summary != nil {
		restored = append([]agentcore.AgentMessage{*summary}, restored...)
	}

	return ContextSnapshot{
		Messages:        restored,
		Provider:        lastProvider,
		Model:           lastModel,
		ReasoningEffort: lastReasoningEffort,
		PlanSlug:        lastPlanSlug,
		PlanPhase:       lastPlanPhase,
		PlanPreMode:     lastPlanPreMode,
		Goal:            lastGoal,
	}, nil
}

// repairInterruptedToolCalls keeps provider message invariants valid while
// preserving the important crash-recovery fact: a missing result does not
// prove that the tool never ran.
func repairInterruptedToolCalls(msgs []agentcore.Message) []agentcore.Message {
	missing := make(map[string]struct{})
	for _, issue := range agentcore.ValidateMessageSequence(msgs) {
		if issue.Kind == agentcore.MessageSequenceIssueMissingToolResult {
			missing[issue.ToolCallID] = struct{}{}
		}
	}

	repaired := agentcore.RepairMessageSequence(msgs)
	if len(missing) == 0 {
		return repaired
	}

	for i, msg := range repaired {
		if msg.Role != agentcore.RoleTool {
			continue
		}
		callID, _ := msg.Metadata["tool_call_id"].(string)
		if _, ok := missing[callID]; !ok {
			continue
		}

		result := agentcore.ToolResultMsg(callID, []byte(interruptedToolResult), true)
		result.Metadata["interrupted"] = true
		result.Metadata["result_unknown"] = true
		repaired[i] = result
		delete(missing, callID)
	}
	return repaired
}
