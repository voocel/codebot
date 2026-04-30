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
type cacheSnapshot struct {
	SystemHash      uint64
	ToolsHash       uint64
	CacheReadTokens int
	Valid           bool // false before the first turn
}

// breakDropFraction is the minimum relative drop in cache_read (vs previous
// turn) that we treat as a "break".
const breakDropFraction = 0.05

// breakDropAbsolute is the minimum absolute token drop to avoid false positives
// at small context sizes.
const breakDropAbsolute = 2000

// hashSystemBlocks returns a stable fingerprint for the current system prompt.
// The fingerprint covers every block's text; cache_control metadata is ignored
// because flipping a cache-control TTL should not look like a content change.
func hashSystemBlocks(blocks []agentcore.SystemBlock) uint64 {
	h := fnv.New64a()
	for _, b := range blocks {
		h.Write([]byte{0}) // block separator
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

	info := &storage.CacheBreakInfo{
		PrevCacheReadTokens: prev.CacheReadTokens,
		CurrCacheReadTokens: curr.CacheReadTokens,
		DropAbsolute:        dropAbs,
		DropFraction:        frac,
		SystemChanged:       prev.SystemHash != curr.SystemHash,
		ToolsChanged:        prev.ToolsHash != curr.ToolsHash,
	}
	switch {
	case info.SystemChanged && info.ToolsChanged:
		info.Note = "system prompt and tool set both changed"
	case info.SystemChanged:
		info.Note = "system prompt changed between turns"
	case info.ToolsChanged:
		info.Note = "tool set changed between turns"
	default:
		info.Note = "no input change detected (TTL expiry, provider-side miss, or cache_control not honored by provider)"
	}
	return info
}
