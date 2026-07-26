package agent

import (
	"hash/fnv"
	"sort"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/storage"
)

// cacheSnapshot captures enough state about one LLM request to diagnose a
// cache-break on the following turn. It does NOT store the request itself —
// only cheap fingerprints of the inputs and the observed cache_read figure.
//
// System blocks are hashed in two halves matching the layout in
// promptState.rebuildLocked: the cached static prefix (block 1 +
// block 2) and the uncached dynamic tail (block 3 if present). Hashing them
// separately lets detectCacheBreak attribute a drop to the right segment —
// a static-prefix change is alarming (it means something supposedly frozen
// moved), a dynamic-block change is routine (plan toggle, MCP refresh).
type cacheSnapshot struct {
	FrozenSystemHash  uint64
	DynamicSystemHash uint64
	ToolsHash         uint64
	CacheReadTokens   int
	Valid             bool // false before the first turn
}

// cacheMonitor guards the running snapshot. The three mutators below are the
// whole write surface, which keeps the snapshot's two halves straight: the
// hashes belong to the prompt-rebuild path, CacheReadTokens / Valid to the
// turn-completion path. Zero value is usable (Valid == false means no baseline).
type cacheMonitor struct {
	mu   sync.Mutex
	snap cacheSnapshot
}

// updateInputHashes leaves CacheReadTokens / Valid alone: a rebuild mid-session
// must keep the previously observed cache_read so the next turn can still
// detect a drop against it.
func (c *cacheMonitor) updateInputHashes(frozen, dynamic, tools uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.FrozenSystemHash = frozen
	c.snap.DynamicSystemHash = dynamic
	c.snap.ToolsHash = tools
}

// invalidateBaseline is called after a compaction rewrites the prompt prefix:
// the cache_read drop that follows is expected, not a break.
func (c *cacheMonitor) invalidateBaseline() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap.CacheReadTokens = 0
	c.snap.Valid = false
}

// observe reads the old baseline and stores the new one in one critical
// section: split them and two turns completing close together can each compare
// against the other's half-written baseline.
func (c *cacheMonitor) observe(cacheRead int) (prev, curr cacheSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev = c.snap
	curr = cacheSnapshot{
		FrozenSystemHash:  c.snap.FrozenSystemHash,
		DynamicSystemHash: c.snap.DynamicSystemHash,
		ToolsHash:         c.snap.ToolsHash,
		CacheReadTokens:   cacheRead,
		Valid:             true,
	}
	c.snap = curr
	return prev, curr
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

// hashFrozenBlocks fingerprints the cache-controlled static prefix (the
// blocks carrying CacheControl != ""). Together these are what the server
// caches; any change here invalidates the prefix on the next turn.
func hashFrozenBlocks(blocks []agentcore.SystemBlock) uint64 {
	h := fnv.New64a()
	for _, b := range blocks {
		if b.CacheControl == "" {
			continue
		}
		h.Write([]byte{0})
		h.Write([]byte(b.Text))
	}
	return h.Sum64()
}

// hashDynamicBlock fingerprints the tail blocks that are NOT cache-controlled
// (MCP tool directory, overlays, git snapshot). Changes here don't reduce
// cache hits on the static prefix but explain visible byte differences in
// the request and any extra write-cost on subsequent breakpoints downstream.
func hashDynamicBlock(blocks []agentcore.SystemBlock) uint64 {
	h := fnv.New64a()
	for _, b := range blocks {
		if b.CacheControl != "" {
			continue
		}
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
		info.Note = "tool set changed between turns"
	default:
		info.Note = "no input change detected (TTL expiry, provider-side miss, or cache_control not honored by provider)"
	}
	return info
}
