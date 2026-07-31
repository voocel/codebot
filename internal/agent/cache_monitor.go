package agent

import (
	"hash/fnv"
	"sort"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/storage"
)

// cacheSnapshot captures enough state about one LLM request to diagnose a
// cache-break on the following turn. It does NOT store the request itself —
// only cheap fingerprints of the inputs and the observed cache_read figure.
//
// System blocks are hashed in two halves split at the last cache_control
// marker (see BuildSystemBlocks for the layout): the cached prefix and the
// uncached tail. Hashing them separately lets detectCacheBreak attribute a
// drop to the right segment — a prefix change is alarming (something
// supposedly frozen moved), a tail change is routine (plan toggle, MCP
// refresh).
type cacheSnapshot struct {
	FrozenSystemHash  uint64
	DynamicSystemHash uint64
	ToolsHash         uint64
	CacheReadTokens   int
	Valid             bool // false before the first turn
	// ExpectedDrop marks a turn we rewrote the prompt ahead of, so the drop it
	// observes is our own doing.
	ExpectedDrop bool
}

// cacheMonitor guards the running snapshot. The three mutators below are the
// whole write surface, which keeps the snapshot's two halves straight: the
// hashes belong to the prompt-rebuild path, CacheReadTokens / Valid to the
// turn-completion path. Zero value is usable (Valid == false means no baseline).
type cacheMonitor struct {
	mu sync.Mutex
	// snap is the last observed turn — the baseline the next one compares
	// against. Its hashes are the ones that turn was actually sent with.
	snap cacheSnapshot
	// pending holds the hashes of the request being built. Kept apart from
	// snap because a rebuild lands between two turns: folding it into snap
	// would overwrite the baseline's hashes with the incoming ones and every
	// comparison would find them equal.
	pending struct{ frozen, dynamic, tools uint64 }
	// lastTurn stamps the last completed turn — i.e. when the server last
	// wrote the prefix. Read by idleFor to decide whether it has expired.
	lastTurn time.Time
	// expectDropNext is armed when we rewrite the prompt and disarmed by the
	// next observe, so exactly one turn is attributed to that rewrite.
	expectDropNext bool
}

// updateInputHashes records what the next request will be built from. It stays
// out of the baseline so the turn after it can be compared against what the
// previous turn actually sent.
func (c *cacheMonitor) updateInputHashes(frozen, dynamic, tools uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending.frozen = frozen
	c.pending.dynamic = dynamic
	c.pending.tools = tools
}

// invalidateBaseline drops the baseline entirely, for when the history it
// described is gone (/clear, /new, /resume). The next turn has nothing
// meaningful to compare against, so it is skipped outright.
//
// Prefer expectDrop for a rewrite inside a continuing conversation: this one
// blinds the following turn completely.
func (c *cacheMonitor) invalidateBaseline() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.CacheReadTokens = 0
	c.snap.Valid = false
}

// expectDrop arms the next observation as self-inflicted: we just rewrote the
// prompt, so the cache_read it reports will be down through no fault of the
// provider.
//
// Unlike invalidateBaseline it keeps the baseline rolling, which matters
// because compaction can fire on consecutive turns under sustained token
// pressure — dropping the baseline each time would leave break detection off
// for the rest of the session, exactly when it is most worth having.
func (c *cacheMonitor) expectDrop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expectDropNext = true
}

// observe reads the old baseline and stores the new one in one critical
// section: split them and two turns completing close together can each compare
// against the other's half-written baseline. now stamps the entry so idleFor
// can tell whether the server-side cache has since expired.
func (c *cacheMonitor) observe(cacheRead int, now time.Time) (prev, curr cacheSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev = c.snap
	curr = cacheSnapshot{
		FrozenSystemHash:  c.pending.frozen,
		DynamicSystemHash: c.pending.dynamic,
		ToolsHash:         c.pending.tools,
		CacheReadTokens:   cacheRead,
		Valid:             true,
		ExpectedDrop:      c.expectDropNext,
	}
	c.expectDropNext = false
	c.snap = curr
	c.lastTurn = now
	return prev, curr
}

// idleFor reports how long the conversation has been idle, and false when no
// turn has completed yet (nothing is cached, so nothing can have expired).
func (c *cacheMonitor) idleFor(now time.Time) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastTurn.IsZero() {
		return 0, false
	}
	return now.Sub(c.lastTurn), true
}

func (c *cacheMonitor) snapshot() cacheSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap
}

// breakDropFraction is the minimum relative drop in cache_read (vs previous
// turn) that we treat as a "break".
const breakDropFraction = 0.05

// breakDropAbsolute is the minimum absolute token drop to avoid false positives
// at small context sizes.
const breakDropAbsolute = 2000

// cachedPrefixLen returns how many leading blocks the server caches: the
// prefix runs through the LAST marker, so an unmarked block between two marked
// ones is cached too. Grouping by "carries a marker" would misfile it as
// dynamic and make detectCacheBreak blame the wrong side.
func cachedPrefixLen(blocks []agentcore.SystemBlock) int {
	cut := 0
	for i, b := range blocks {
		if b.CacheControl != "" {
			cut = i + 1
		}
	}
	return cut
}

// hashFrozenBlocks fingerprints the cached prefix. Any change here invalidates
// it on the next turn.
func hashFrozenBlocks(blocks []agentcore.SystemBlock) uint64 {
	return hashBlockTexts(blocks[:cachedPrefixLen(blocks)])
}

// hashDynamicBlock fingerprints the tail past the last breakpoint (MCP tool
// directory, overlays). Changes here don't reduce cache hits on the prefix but
// explain visible byte differences in the request.
func hashDynamicBlock(blocks []agentcore.SystemBlock) uint64 {
	return hashBlockTexts(blocks[cachedPrefixLen(blocks):])
}

func hashBlockTexts(blocks []agentcore.SystemBlock) uint64 {
	h := fnv.New64a()
	for _, b := range blocks {
		h.Write([]byte{0})
		h.Write([]byte(b.Text))
	}
	return h.Sum64()
}

// hashTools returns a stable fingerprint for the current tool set as the LLM
// sees it: sorted by name, each entry contributes name + description. Schema
// is excluded — changing it means breaking cache, and we want the fingerprint
// to reflect "same name, same caller contract" rather than incidental schema
// reorderings.
func hashTools(tools []agentcore.Tool) uint64 {
	names := make([]string, 0, len(tools))
	byName := make(map[string]agentcore.Tool, len(tools))
	for _, t := range tools {
		n := t.Name()
		if _, seen := byName[n]; seen {
			continue
		}
		byName[n] = t
		names = append(names, n)
	}
	sort.Strings(names)

	h := fnv.New64a()
	for _, n := range names {
		h.Write([]byte{0})
		h.Write([]byte(n))
		h.Write([]byte{1})
		h.Write([]byte(byName[n].Description()))
	}
	return h.Sum64()
}

// detectCacheBreak decides whether the observed cache_read drop between
// snapshots is large enough to record, and if so produces a structured
// explanation. Returns nil when no break is detected.
func detectCacheBreak(prev, curr cacheSnapshot) *storage.CacheBreakInfo {
	if !prev.Valid {
		return nil
	}
	// A turn with zero previous cache_read has no baseline to drop from.
	if prev.CacheReadTokens <= 0 {
		return nil
	}
	dropAbs := prev.CacheReadTokens - curr.CacheReadTokens
	if dropAbs < breakDropAbsolute {
		return nil
	}
	frac := float64(dropAbs) / float64(prev.CacheReadTokens)
	if frac < breakDropFraction {
		return nil
	}

	// A rewrite we performed ourselves explains the drop on its own; reading
	// the hashes past this point would blame whatever else happened to move.
	// Recorded rather than suppressed — this entry is the only trace a
	// projected rewrite leaves, since those never reach the store.
	if curr.ExpectedDrop {
		return &storage.CacheBreakInfo{
			PrevCacheReadTokens: prev.CacheReadTokens,
			CurrCacheReadTokens: curr.CacheReadTokens,
			DropAbsolute:        dropAbs,
			DropFraction:        frac,
			Expected:            true,
			Note:                "prompt rewritten by compaction (expected drop, not a break)",
		}
	}

	frozenChanged := prev.FrozenSystemHash != curr.FrozenSystemHash
	dynamicChanged := prev.DynamicSystemHash != curr.DynamicSystemHash
	info := &storage.CacheBreakInfo{
		PrevCacheReadTokens: prev.CacheReadTokens,
		CurrCacheReadTokens: curr.CacheReadTokens,
		DropAbsolute:        dropAbs,
		DropFraction:        frac,
		SystemChanged:       frozenChanged || dynamicChanged,
		FrozenChanged:       frozenChanged,
		DynamicChanged:      dynamicChanged,
		ToolsChanged:        prev.ToolsHash != curr.ToolsHash,
	}
	switch {
	case frozenChanged && info.ToolsChanged:
		info.Note = "frozen system prefix and tool set both changed (unexpected — investigate)"
	case frozenChanged:
		info.Note = "frozen system prefix changed (unexpected — investigate)"
	case dynamicChanged && info.ToolsChanged:
		info.Note = "dynamic system block and tool set changed (likely MCP/plan-mode toggle)"
	case dynamicChanged:
		info.Note = "dynamic system block changed (plan mode / MCP overlay)"
	case info.ToolsChanged:
		info.Note = "tool set changed between turns (tools precede system in the cache prefix, so this cascades)"
	default:
		info.Note = "no input change detected (TTL expiry, provider-side miss, or cache_control not honored by provider)"
	}
	return info
}
