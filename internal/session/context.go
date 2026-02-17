package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/voocel/agentcore"
)

// ContextSnapshot is the projected runtime state from a session log.
type ContextSnapshot struct {
	Messages []agentcore.AgentMessage
	Provider string
	Model    string
	Thinking string
}

// BuildSnapshot reconstructs runtime state by walking the tree from the current leaf.
func (s *Store) BuildSnapshot() (ContextSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if err != nil {
		return ContextSnapshot{}, err
	}
	defer f.Close()

	// Read all entries into a map.
	entries := make(map[string]Entry)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry Entry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.ID != "" {
			if _, exists := entries[entry.ID]; exists {
				return ContextSnapshot{}, fmt.Errorf("duplicate entry id: %s", entry.ID)
			}
			entries[entry.ID] = entry
		}
	}
	if err := scanner.Err(); err != nil {
		return ContextSnapshot{}, err
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

	// Build context from the chain.
	var msgs []agentcore.AgentMessage
	lastProvider := ""
	lastModel := ""
	lastThinking := ""

	for _, entry := range chain {
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
		case EntryThinkingChange:
			var tc ThinkingLevelChange
			if json.Unmarshal(entry.Data, &tc) == nil {
				lastThinking = tc.Level
			}
		case EntryCompaction:
			var c Compaction
			if json.Unmarshal(entry.Data, &c) == nil {
				// Compaction replaces all prior messages with summary + kept messages.
				msgs = nil
				if c.Summary != "" {
					msgs = append(msgs, agentcore.Message{
						Role:      agentcore.RoleUser,
						Content:   []agentcore.ContentBlock{agentcore.TextBlock(c.Summary)},
						Timestamp: entry.Timestamp,
					})
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
		}
	}

	return ContextSnapshot{
		Messages: msgs,
		Provider: lastProvider,
		Model:    lastModel,
		Thinking: lastThinking,
	}, nil
}
