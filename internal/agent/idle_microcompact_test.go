package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/codebot/internal/config"
)

// toolExchange builds one assistant tool_call + its tool result, the shape
// findCompactableToolResults walks.
func toolExchange(callID, tool, output string) []agentcore.AgentMessage {
	call := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{{
			Type: agentcore.ContentToolCall,
			ToolCall: &agentcore.ToolCall{
				ID:   callID,
				Name: tool,
				Args: json.RawMessage(`{"path":"` + callID + `"}`),
			},
		}},
	}
	result := agentcore.Message{
		Role:     agentcore.RoleTool,
		Content:  []agentcore.ContentBlock{agentcore.TextBlock(output)},
		Metadata: map[string]any{"tool_call_id": callID},
	}
	return []agentcore.AgentMessage{call, result}
}

func newIdleTestSession(t *testing.T) *Session {
	t.Helper()
	s := NewSession(SessionConfig{
		Agent:    agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Settings: config.Resolved{MaxTurns: 10},
		Cwd:      t.TempDir(),
		ToolMicrocompact: agentctx.NewToolResultMicrocompact(agentctx.ToolResultMicrocompactConfig{
			Classifier: CodebotToolClassifier,
			KeepRecent: 1,
		}),
	})
	t.Cleanup(s.Close)
	return s
}

func seedToolHistory(t *testing.T, s *Session, n int) {
	t.Helper()
	var msgs []agentcore.AgentMessage
	for i := range n {
		msgs = append(msgs, toolExchange(string(rune('a'+i)), "read", strings.Repeat("payload ", 50))...)
	}
	if err := s.deps.agent.SetMessages(msgs); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
}

func countCleared(s *Session) int {
	n := 0
	for _, am := range s.deps.agent.Messages() {
		msg, ok := am.(agentcore.Message)
		if !ok || msg.Role != agentcore.RoleTool {
			continue
		}
		for _, b := range msg.Content {
			if b.Type == agentcore.ContentText && strings.Contains(b.Text, "cleared") {
				n++
			}
		}
	}
	return n
}

// The whole premise: cleanup is free only once the prefix has expired, so a
// warm cache must be left alone even though the same tool results are stale.
func TestIdleMicrocompactSkipsWhileCacheIsWarm(t *testing.T) {
	t.Parallel()

	s := newIdleTestSession(t)
	seedToolHistory(t, s, 4)

	now := time.Now()
	s.cache.observe(50_000, now)

	if s.idleCompact.run(s, now.Add(promptCacheTTL-time.Second)) {
		t.Fatal("ran while the cache was still warm — that costs a write it should not")
	}
	if got := countCleared(s); got != 0 {
		t.Fatalf("%d tool results cleared with a warm cache, want 0", got)
	}
}

func TestIdleMicrocompactClearsOnceCacheExpired(t *testing.T) {
	t.Parallel()

	s := newIdleTestSession(t)
	seedToolHistory(t, s, 4)

	now := time.Now()
	s.cache.observe(50_000, now)

	if !s.idleCompact.run(s, now.Add(promptCacheTTL+time.Second)) {
		t.Fatal("did not run after the cache expired")
	}
	// KeepRecent=1 protects the newest result; the other three go.
	if got := countCleared(s); got != 3 {
		t.Fatalf("cleared %d tool results, want 3 (4 seeded, 1 protected)", got)
	}
	// The expiry was going to rewrite the prefix anyway — the coming
	// cache_read drop belongs to this cleanup, not to the provider.
	prev, curr := s.cache.observe(1_000, now.Add(promptCacheTTL+2*time.Second))
	if !curr.ExpectedDrop {
		t.Fatal("cleanup did not claim the drop it is about to cause")
	}
	if info := detectCacheBreak(prev, curr); info == nil || !info.Expected {
		t.Fatalf("drop not attributed to the rewrite: %+v", info)
	}
}

// No completed turn means nothing was ever cached, so nothing can have expired.
func TestIdleMicrocompactSkipsBeforeFirstTurn(t *testing.T) {
	t.Parallel()

	s := newIdleTestSession(t)
	seedToolHistory(t, s, 4)

	if s.idleCompact.run(s, time.Now().Add(24*time.Hour)) {
		t.Fatal("ran before any turn completed")
	}
}

func TestIdleMicrocompactDisabledWithoutStrategy(t *testing.T) {
	t.Parallel()

	if newIdleMicrocompact(nil) != nil {
		t.Fatal("a nil strategy must disable the feature, not build a runner")
	}
	var m *idleMicrocompact
	if m.run(nil, time.Now()) {
		t.Fatal("nil runner must be a no-op")
	}
}
