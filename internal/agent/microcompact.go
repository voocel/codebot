package agent

import (
	"fmt"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/memory"
)

const (
	microcompactThreshold    = 40000 // estimated tokens to trigger
	microcompactMinSaving    = 20000 // minimum token savings to proceed
	largeTextThreshold       = 4000  // chars to consider "large"
	microcompactPreserveHead = 1500  // chars to keep from start
	microcompactPreserveTail = 1500  // chars to keep from end
	microcompactKeepRecent   = 3     // protect last N large-text messages
)

// microcompact performs zero-cost local truncation of old large text content
// in the conversation history. Unlike LLM-based compaction, this does not
// call any model — it simply trims oversized text blocks in older messages.
// Returns true if any truncation was performed.
func (s *Session) microcompact() bool {
	msgs := s.agent.Messages()
	totalTokens := memory.EstimateTotal(msgs)
	if totalTokens < microcompactThreshold {
		return false
	}

	// Find messages with large text content.
	type candidate struct {
		idx     int
		savings int // estimated token savings
	}
	var candidates []candidate
	for i, m := range msgs {
		msg, ok := m.(agentcore.Message)
		if !ok {
			continue
		}
		for _, b := range msg.Content {
			if b.Type == agentcore.ContentText && len([]rune(b.Text)) > largeTextThreshold {
				// Use len(b.Text) (bytes) to match memory.EstimateTokens which uses byte count.
				saved := len(b.Text) - microcompactPreserveHead*3 // conservative for multi-byte
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

	// Protect the most recent N large-text messages.
	protectCount := min(microcompactKeepRecent, len(candidates))
	eligible := candidates[:len(candidates)-protectCount]
	if len(eligible) == 0 {
		return false
	}

	// Check if savings are worth it.
	var totalSavings int
	for _, c := range eligible {
		totalSavings += c.savings
	}
	if totalSavings < microcompactMinSaving {
		return false
	}

	// Build set of eligible indices for O(1) lookup.
	eligibleSet := make(map[int]struct{}, len(eligible))
	for _, c := range eligible {
		eligibleSet[c.idx] = struct{}{}
	}

	// Build new message list with truncated content.
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

	if err := s.agent.SetMessages(newMsgs); err != nil {
		return false
	}
	return true
}

// truncateMessageText truncates large text blocks in a message while
// preserving all other content (thinking, tool calls, images) and metadata.
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
