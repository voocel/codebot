package agent

import (
	"testing"

	"github.com/voocel/agentcore"
)

func TestHashSystemBlocksStableAcrossCallOrder(t *testing.T) {
	t.Parallel()

	a := []agentcore.SystemBlock{{Text: "identity"}, {Text: "instructions"}}
	b := []agentcore.SystemBlock{{Text: "identity"}, {Text: "instructions"}}
	if hashSystemBlocks(a) != hashSystemBlocks(b) {
		t.Fatalf("identical blocks must hash to the same value")
	}

	c := []agentcore.SystemBlock{{Text: "identity"}, {Text: "instructions v2"}}
	if hashSystemBlocks(a) == hashSystemBlocks(c) {
		t.Fatalf("differing text must change the hash")
	}
}

func TestHashSystemBlocksIgnoresCacheControl(t *testing.T) {
	t.Parallel()

	a := []agentcore.SystemBlock{{Text: "identity", CacheControl: "ephemeral"}}
	b := []agentcore.SystemBlock{{Text: "identity"}}
	if hashSystemBlocks(a) != hashSystemBlocks(b) {
		t.Fatalf("cache_control metadata must not influence the fingerprint")
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
	curr := cacheSnapshot{Valid: true, CacheReadTokens: 96000, SystemHash: prev.SystemHash}
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

func TestDetectCacheBreakReportsSystemChange(t *testing.T) {
	t.Parallel()

	prev := cacheSnapshot{Valid: true, SystemHash: 1, ToolsHash: 2, CacheReadTokens: 50000}
	curr := cacheSnapshot{Valid: true, SystemHash: 9, ToolsHash: 2, CacheReadTokens: 0}
	info := detectCacheBreak(prev, curr)
	if info == nil {
		t.Fatal("expected a cache break to be reported")
	}
	if !info.SystemChanged || info.ToolsChanged {
		t.Fatalf("expected system-only change, got %+v", info)
	}
	if info.DropAbsolute != 50000 {
		t.Fatalf("drop_absolute = %d, want 50000", info.DropAbsolute)
	}
	if info.Note == "" {
		t.Fatal("note should be populated so logs are self-describing")
	}
}

func TestDetectCacheBreakReportsUnknownWhenHashesMatch(t *testing.T) {
	t.Parallel()

	prev := cacheSnapshot{Valid: true, SystemHash: 1, ToolsHash: 2, CacheReadTokens: 80000}
	curr := cacheSnapshot{Valid: true, SystemHash: 1, ToolsHash: 2, CacheReadTokens: 0}
	info := detectCacheBreak(prev, curr)
	if info == nil {
		t.Fatal("expected a break to be reported even without hash diffs")
	}
	if info.SystemChanged || info.ToolsChanged {
		t.Fatalf("expected neither hash to flip, got %+v", info)
	}
}

func TestCompactionEventInvalidatesCacheBaseline(t *testing.T) {
	t.Parallel()

	s := &Session{cacheSnap: cacheSnapshot{Valid: true, CacheReadTokens: 50000, SystemHash: 1}}

	// An "unchanged" compaction must not reset the baseline — no rewrite happened.
	s.emit(SessionEvent{Type: SEAutoCompactionEnd, CompactionChanged: false})
	if !s.cacheSnap.Valid || s.cacheSnap.CacheReadTokens != 50000 {
		t.Fatalf("unchanged compaction should preserve baseline, got %+v", s.cacheSnap)
	}

	// A real compaction rewrites the prefix; the baseline must be dropped so
	// the next turn's expected cache_read drop is not misreported.
	s.emit(SessionEvent{Type: SEAutoCompactionEnd, CompactionChanged: true})
	if s.cacheSnap.Valid || s.cacheSnap.CacheReadTokens != 0 {
		t.Fatalf("changed compaction should invalidate baseline, got %+v", s.cacheSnap)
	}
}
