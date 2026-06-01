package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/voocel/agentcore"
)

// TranscriptStore persists each teammate's conversation as an append-only
// JSONL file at <dir>/<agentName>.jsonl — one marshaled agentcore.Message per
// line, in conversation order. It is the durable counterpart to the in-memory
// TeammateEventHub: the hub is lossy (drop-oldest, for live UI), whereas this
// captures every turn losslessly so a restarted session can rebuild a
// teammate's full context.
//
// Append is driven from the teammate's turn executor (the same messages the
// team runner stitches into its history), so persistence and the live loop
// see identical bytes. Load + RepairMessageSequence reconstruct a valid
// sequence even when a crash left a half-written tool call at the tail.
type TranscriptStore struct {
	mu  sync.Mutex
	dir string
}

// NewTranscriptStore returns a store rooted at dir (typically
// config.TeamDir(sessionID)/transcripts). A "" dir disables persistence —
// every method becomes a no-op — so callers need not special-case it.
func NewTranscriptStore(dir string) *TranscriptStore {
	return &TranscriptStore{dir: dir}
}

func (s *TranscriptStore) path(agent string) string {
	return filepath.Join(s.dir, agent+".jsonl")
}

// Append writes each message as one JSON line, creating the dir/file on first
// use. Append-only: prior turns are never rewritten, so a resumed teammate's
// new turns extend the existing transcript rather than duplicating loaded
// history. Safe for concurrent calls across different agents (one mutex
// serialises the rare per-turn writes).
func (s *TranscriptStore) Append(agent string, msgs []agentcore.AgentMessage) error {
	if s == nil || s.dir == "" || agent == "" || len(msgs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("transcript dir: %w", err)
	}
	f, err := os.OpenFile(s.path(agent), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, m := range msgs {
		if m == nil {
			continue
		}
		data, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("marshal transcript message: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return w.Flush()
}

// Load reads <agent>.jsonl and returns the repaired message sequence ready to
// seed a resumed teammate via team.SpawnConfig.History. A missing file yields
// (nil, nil). Malformed lines are skipped — a crash can leave a torn final
// line — and RepairMessageSequence drops any dangling tool_use the crash left
// without a matching result.
func (s *TranscriptStore) Load(agent string) ([]agentcore.AgentMessage, error) {
	if s == nil || s.dir == "" || agent == "" {
		return nil, nil
	}
	f, err := os.Open(s.path(agent))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var msgs []agentcore.Message
	sc := bufio.NewScanner(f)
	// Tool outputs can be large; raise the line cap well above the default 64KB.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m agentcore.Message
		if json.Unmarshal(line, &m) != nil {
			continue // torn or malformed tail line — skip
		}
		msgs = append(msgs, m)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	repaired := agentcore.RepairMessageSequence(msgs)
	return agentcore.ToAgentMessages(repaired), nil
}
