package agent

import (
	"hash/fnv"
	"sort"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/storage"
)

// cacheSnapshot captures enough state about one LLM request to diagnose a
// cache-break on the following turn. It does NOT store the request itself —
// only cheap fingerprints of the inputs and the observed cache_read figure.
//
// System blocks are hashed in two halves matching the layout in
// sessionPromptManager.rebuildPrompt: the cached static prefix (block 1 +
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
