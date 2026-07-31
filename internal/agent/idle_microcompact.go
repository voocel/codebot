package agent

import (
	"context"
	"time"

	agentctx "github.com/voocel/agentcore/context"
)

// promptCacheTTL is how long the provider keeps the prefix we paid to write.
// agentcore emits cache_control "ephemeral" with no ttl, which Anthropic reads
// as 5 minutes (the "ephemeral:1h" form would buy an hour). Deriving the idle
// threshold from the TTL is the whole point — Claude Code uses 60 minutes
// because it writes 1h entries, and copying that number here would mean almost
// every expiry went unnoticed.
const promptCacheTTL = 5 * time.Minute

// idleMicrocompact clears stale tool results when the prompt cache has already
// expired, before the next request goes out.
//
// The trade normally blocking this is gone: rewriting history invalidates the
// prefix, so cleanup usually costs a full cache write. Once the entry has aged
// out there is nothing left to invalidate — the prefix gets rewritten either
// way, and shrinking it first makes that rewrite cheaper. The window is only
// open before the request; running after the miss would help nobody.
//
// Deliberately NOT routed through ContextEngine.Compact: that forces the whole
// strategy chain and would fire a synchronous LLM summary. This path is free.
type idleMicrocompact struct {
	strategy  *agentctx.ToolResultMicrocompactStrategy
	threshold time.Duration
}

func newIdleMicrocompact(strategy *agentctx.ToolResultMicrocompactStrategy) *idleMicrocompact {
	if strategy == nil {
		return nil
	}
	return &idleMicrocompact{strategy: strategy, threshold: promptCacheTTL}
}

// run reports whether it rewrote the conversation. Called at a turn boundary
// from the prompt path, so mutating the agent's messages here cannot race a
// live run.
func (m *idleMicrocompact) run(s *Session, now time.Time) bool {
	if m == nil || m.strategy == nil {
		return false
	}
	idle, ok := s.cache.idleFor(now)
	if !ok || idle < m.threshold {
		return false
	}

	msgs := s.deps.agent.Messages()
	if len(msgs) == 0 {
		return false
	}
	// Apply ignores the transcript and budget arguments (see
	// ToolResultMicrocompactStrategy.Apply) — it protects the most recent
	// KeepRecent results and clears the rest, which is exactly the free
	// cleanup wanted here.
	next, result, err := m.strategy.Apply(context.Background(), nil, msgs, agentctx.Budget{})
	if err != nil || !result.Applied {
		return false
	}
	if err := s.deps.agent.SetMessages(next); err != nil {
		return false
	}
	// The prefix was going to be rewritten by the expiry anyway; tell the
	// monitor so the coming cache_read drop is attributed here, not to the
	// provider.
	s.cache.expectDrop()
	return true
}
