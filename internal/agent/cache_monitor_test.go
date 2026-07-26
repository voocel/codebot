package agent

import (
	"testing"

	"github.com/voocel/agentcore"
)

func TestHashFrozenBlocksStableAcrossCallOrder(t *testing.T) {
	t.Parallel()

	a := []agentcore.SystemBlock{
		{Text: "identity", CacheControl: "ephemeral"},
		{Text: "instructions", CacheControl: "ephemeral"},
	}
	b := []agentcore.SystemBlock{
		{Text: "identity", CacheControl: "ephemeral"},
		{Text: "instructions", CacheControl: "ephemeral"},
	}
	if hashFrozenBlocks(a) != hashFrozenBlocks(b) {
		t.Fatalf("identical frozen blocks must hash to the same value")
	}

	c := []agentcore.SystemBlock{
		{Text: "identity", CacheControl: "ephemeral"},
		{Text: "instructions v2", CacheControl: "ephemeral"},
	}
	if hashFrozenBlocks(a) == hashFrozenBlocks(c) {
		t.Fatalf("differing text must change the hash")
	}
}

func TestHashFrozenBlocksSkipsUncontrolled(t *testing.T) {
	t.Parallel()

	withTail := []agentcore.SystemBlock{
		{Text: "identity", CacheControl: "ephemeral"},
		{Text: "instructions", CacheControl: "ephemeral"},
		{Text: "dynamic"}, // no cache_control → not part of frozen prefix
	}
	withoutTail := []agentcore.SystemBlock{
		{Text: "identity", CacheControl: "ephemeral"},
		{Text: "instructions", CacheControl: "ephemeral"},
	}
	if hashFrozenBlocks(withTail) != hashFrozenBlocks(withoutTail) {
		t.Fatalf("dynamic tail must not influence the frozen fingerprint")
	}
}

func TestHashDynamicBlockTracksTail(t *testing.T) {
	t.Parallel()

	base := []agentcore.SystemBlock{
		{Text: "identity", CacheControl: "ephemeral"},
		{Text: "instructions", CacheControl: "ephemeral"},
	}
	withA := append(base, agentcore.SystemBlock{Text: "overlay A"})
	withB := append(base, agentcore.SystemBlock{Text: "overlay B"})

	if hashDynamicBlock(withA) == hashDynamicBlock(withB) {
		t.Fatalf("differing tail must change the dynamic hash")
	}
	if hashDynamicBlock(base) == hashDynamicBlock(withA) {
		t.Fatalf("absence vs presence of tail must differ")
	}
}

func TestHashToolsSortsByName(t *testing.T) {
	t.Parallel()

	forward := []agentcore.Tool{&stubTool{name: "read", desc: "read files"}, &stubTool{name: "bash", desc: "run shell"}}
	reverse := []agentcore.Tool{&stubTool{name: "bash", desc: "run shell"}, &stubTool{name: "read", desc: "read files"}}
	if hashTools(forward) != hashTools(reverse) {
		t.Fatalf("tool ordering must not affect the fingerprint")
	}
}

func TestDetectCacheBreakNilWhenNoBaseline(t *testing.T) {
	t.Parallel()

	info := detectCacheBreak(cacheSnapshot{Valid: false}, cacheSnapshot{Valid: true, CacheReadTokens: 5000})
	if info != nil {
		t.Fatalf("no baseline should suppress detection, got %+v", info)
	}
}

func TestDetectCacheBreakIgnoresSmallDrops(t *testing.T) {
	t.Parallel()

	// 4% drop, well below breakDropFraction.
	prev := cacheSnapshot{Valid: true, CacheReadTokens: 100000}
	curr := cacheSnapshot{Valid: true, CacheReadTokens: 96000}
	if info := detectCacheBreak(prev, curr); info != nil {
		t.Fatalf("small drop should not trigger, got %+v", info)
	}

	// 10% drop but absolute delta below 2000.
	prev = cacheSnapshot{Valid: true, CacheReadTokens: 10000}
	curr = cacheSnapshot{Valid: true, CacheReadTokens: 9000}
	if info := detectCacheBreak(prev, curr); info != nil {
		t.Fatalf("absolute drop below threshold should not trigger, got %+v", info)
	}
}

func TestDetectCacheBreakReportsFrozenChange(t *testing.T) {
	t.Parallel()

	prev := cacheSnapshot{Valid: true, FrozenSystemHash: 1, DynamicSystemHash: 7, ToolsHash: 2, CacheReadTokens: 50000}
	curr := cacheSnapshot{Valid: true, FrozenSystemHash: 9, DynamicSystemHash: 7, ToolsHash: 2, CacheReadTokens: 0}
	info := detectCacheBreak(prev, curr)
	if info == nil {
		t.Fatal("expected a cache break to be reported")
	}
	if !info.FrozenChanged {
		t.Fatalf("expected frozen-prefix flag set, got %+v", info)
	}
	if info.DynamicChanged || info.ToolsChanged {
		t.Fatalf("only frozen should flip, got %+v", info)
	}
	if !info.SystemChanged {
		t.Fatal("legacy SystemChanged must reflect any system change")
	}
	if info.DropAbsolute != 50000 {
		t.Fatalf("drop_absolute = %d, want 50000", info.DropAbsolute)
	}
	if info.Note == "" {
		t.Fatal("note should be populated so logs are self-describing")
	}
}

func TestDetectCacheBreakReportsDynamicChange(t *testing.T) {
	t.Parallel()

	prev := cacheSnapshot{Valid: true, FrozenSystemHash: 1, DynamicSystemHash: 7, ToolsHash: 2, CacheReadTokens: 50000}
	curr := cacheSnapshot{Valid: true, FrozenSystemHash: 1, DynamicSystemHash: 8, ToolsHash: 2, CacheReadTokens: 0}
	info := detectCacheBreak(prev, curr)
	if info == nil {
		t.Fatal("expected a cache break to be reported")
	}
	if info.FrozenChanged {
		t.Fatalf("frozen prefix must not be flagged when only dynamic changed: %+v", info)
	}
	if !info.DynamicChanged {
		t.Fatalf("expected dynamic flag set, got %+v", info)
	}
}

func TestDetectCacheBreakReportsUnknownWhenHashesMatch(t *testing.T) {
	t.Parallel()

	prev := cacheSnapshot{Valid: true, FrozenSystemHash: 1, DynamicSystemHash: 2, ToolsHash: 3, CacheReadTokens: 80000}
	curr := cacheSnapshot{Valid: true, FrozenSystemHash: 1, DynamicSystemHash: 2, ToolsHash: 3, CacheReadTokens: 0}
	info := detectCacheBreak(prev, curr)
	if info == nil {
		t.Fatal("expected a break to be reported even without hash diffs")
	}
	if info.SystemChanged || info.FrozenChanged || info.DynamicChanged || info.ToolsChanged {
		t.Fatalf("expected no hash flips, got %+v", info)
	}
}

func TestCompactionEventInvalidatesCacheBaseline(t *testing.T) {
	t.Parallel()

	// Seed the baseline the way a real turn does: a prompt rebuild sets the
	// input hashes, then the finished turn reports its cache_read.
	s := &Session{}
	s.cache.updateInputHashes(1, 0, 0)
	s.cache.observe(50000)

	// An "unchanged" compaction must not reset the baseline — no rewrite happened.
	s.emit(SessionEvent{Type: SEAutoCompactionEnd, CompactionChanged: false})
	if snap := s.cache.snapshot(); !snap.Valid || snap.CacheReadTokens != 50000 {
		t.Fatalf("unchanged compaction should preserve baseline, got %+v", snap)
	}

	// A real compaction rewrites the prefix; the baseline must be dropped so
	// the next turn's expected cache_read drop is not misreported.
	s.emit(SessionEvent{Type: SEAutoCompactionEnd, CompactionChanged: true})
	if snap := s.cache.snapshot(); snap.Valid || snap.CacheReadTokens != 0 {
		t.Fatalf("changed compaction should invalidate baseline, got %+v", snap)
	}
}
