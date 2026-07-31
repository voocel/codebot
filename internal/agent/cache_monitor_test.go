package agent

import (
	"testing"
	"time"

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

func TestCompactionEventAttributesTheNextDrop(t *testing.T) {
	t.Parallel()

	// Seed the baseline the way a real turn does: a prompt rebuild sets the
	// input hashes, then the finished turn reports its cache_read.
	s := &Session{}
	s.cache.updateInputHashes(1, 0, 0)
	s.cache.observe(50000, time.Now())

	// An "unchanged" compaction must not arm anything — no rewrite happened.
	s.emit(SessionEvent{Type: SEAutoCompactionEnd, CompactionChanged: false})
	prev, curr := s.cache.observe(50000, time.Now())
	if curr.ExpectedDrop {
		t.Fatal("unchanged compaction armed an attribution it did not earn")
	}
	if !prev.Valid || prev.CacheReadTokens != 50000 {
		t.Fatalf("baseline lost, got %+v", prev)
	}

	// A real compaction rewrites the prefix. The baseline must keep rolling —
	// only the attribution of the next drop changes.
	s.emit(SessionEvent{Type: SEAutoCompactionEnd, CompactionChanged: true})
	prev, curr = s.cache.observe(10000, time.Now())
	if !curr.ExpectedDrop {
		t.Fatal("compaction did not arm the drop attribution")
	}
	if !prev.Valid || prev.CacheReadTokens != 50000 {
		t.Fatalf("baseline was dropped instead of kept, got %+v", prev)
	}

	info := detectCacheBreak(prev, curr)
	if info == nil || !info.Expected {
		t.Fatalf("drop not recorded as expected: %+v", info)
	}

	// One turn only: the following drop is unexplained and must be flagged.
	prev, curr = s.cache.observe(1000, time.Now())
	if curr.ExpectedDrop {
		t.Fatal("attribution leaked into a second turn")
	}
	if info := detectCacheBreak(prev, curr); info == nil || info.Expected {
		t.Fatalf("second drop should be a real break, got %+v", info)
	}
}

// The hash comparison only means anything if the baseline keeps the hashes the
// previous turn was sent with. A rebuild lands between turns, so folding it
// into the baseline makes prev and curr always agree and every break falls
// through to the "no input change" catch-all. Driving the real monitor is what
// catches that — building two snapshots by hand tests the pure function and
// skips the bug entirely.
func TestMonitorComparesAgainstThePreviousTurnsHashes(t *testing.T) {
	t.Parallel()

	var c cacheMonitor
	c.updateInputHashes(1, 7, 2)
	c.observe(50000, time.Now())

	// Next turn rebuilds the prompt with a changed frozen prefix.
	c.updateInputHashes(9, 7, 2)
	prev, curr := c.observe(1000, time.Now())

	info := detectCacheBreak(prev, curr)
	if info == nil {
		t.Fatal("no break reported for a changed frozen prefix")
	}
	if !info.FrozenChanged {
		t.Fatalf("frozen change not attributed, got %+v", info)
	}
	if info.DynamicChanged || info.ToolsChanged {
		t.Fatalf("only the frozen prefix moved, got %+v", info)
	}
}

// A rebuild that changes nothing must not manufacture a difference.
func TestMonitorReportsNoInputChangeWhenNothingMoved(t *testing.T) {
	t.Parallel()

	var c cacheMonitor
	c.updateInputHashes(1, 7, 2)
	c.observe(50000, time.Now())

	c.updateInputHashes(1, 7, 2)
	prev, curr := c.observe(1000, time.Now())

	info := detectCacheBreak(prev, curr)
	if info == nil {
		t.Fatal("a large unexplained drop should still be reported")
	}
	if info.FrozenChanged || info.DynamicChanged || info.ToolsChanged {
		t.Fatalf("invented an input change, got %+v", info)
	}
}

// Sustained token pressure fires compaction on consecutive turns. Dropping the
// baseline each time would leave detection off for the rest of the session —
// the case this whole mechanism exists to avoid.
func TestRepeatedCompactionKeepsBreakDetectionAlive(t *testing.T) {
	t.Parallel()

	s := &Session{}
	s.cache.updateInputHashes(1, 0, 0)
	s.cache.observe(50000, time.Now())

	for range 5 {
		s.emit(SessionEvent{Type: SEAutoCompactionEnd, CompactionChanged: true})
		s.cache.observe(50000, time.Now())
	}

	// A genuine break right after that run must still surface.
	s.cache.updateInputHashes(999, 0, 0)
	prev, curr := s.cache.observe(1000, time.Now())
	info := detectCacheBreak(prev, curr)
	if info == nil {
		t.Fatal("break detection went dead after repeated compactions")
	}
	if info.Expected || !info.FrozenChanged {
		t.Fatalf("misattributed the break: %+v", info)
	}
}

// --- system block layout ----------------------------------------------------

func TestBuildSystemBlocksPutsStaticContentInsideThePrefix(t *testing.T) {
	t.Parallel()

	blocks := BuildSystemBlocks("identity", "instructions", "git snapshot", "dynamic")
	if len(blocks) != 4 {
		t.Fatalf("got %d blocks, want 4: %+v", len(blocks), blocks)
	}

	// The git snapshot is collected once at startup and never recomputed, so it
	// must sit ahead of the volatile dynamic block or it can never be cached.
	texts := make([]string, len(blocks))
	for i, b := range blocks {
		texts[i] = b.Text
	}
	want := []string{"identity", "instructions", "git snapshot", "dynamic"}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("block order = %v, want %v", texts, want)
		}
	}

	cut := cachedPrefixLen(blocks)
	if cut != 3 {
		t.Fatalf("cached prefix covers %d blocks, want 3 (everything but dynamic)", cut)
	}
	if blocks[3].CacheControl != "" {
		t.Fatal("dynamic block must not carry a breakpoint — it is invalidated too often to be worth 1.25x")
	}
}

func TestBuildSystemBlocksKeepsIdentityBreakpointForReloads(t *testing.T) {
	t.Parallel()

	before := BuildSystemBlocks("identity", "instructions v1", "git", "")
	after := BuildSystemBlocks("identity", "instructions v2", "git", "")

	// A reload rewrites block 2, which invalidates everything from there on.
	// The separate breakpoint on identity is what survives it.
	if before[0].CacheControl == "" || after[0].CacheControl == "" {
		t.Fatal("identity lost its own breakpoint; a reload would now rewrite the whole prefix")
	}
	if hashFrozenBlocks(before) == hashFrozenBlocks(after) {
		t.Fatal("changed instructions must move the frozen fingerprint")
	}
}

func TestBuildSystemBlocksSkipsEmptyParts(t *testing.T) {
	t.Parallel()

	// SYSTEM.md override: no identity block at all.
	override := BuildSystemBlocks("", "override body", "", "")
	if len(override) != 1 || override[0].Text != "override body" {
		t.Fatalf("override layout = %+v, want a single block", override)
	}
	if override[0].CacheControl == "" {
		t.Fatal("the only static block must still carry the breakpoint")
	}

	// Not a git repo: the breakpoint falls back onto instructions.
	noGit := BuildSystemBlocks("identity", "instructions", "", "dynamic")
	if cachedPrefixLen(noGit) != 2 {
		t.Fatalf("cached prefix covers %d blocks, want 2", cachedPrefixLen(noGit))
	}

	// Dynamic-only: no static content to anchor a breakpoint, but the block
	// still has to reach the model.
	dynOnly := BuildSystemBlocks("", "", "", "dynamic")
	if len(dynOnly) != 1 || dynOnly[0].Text != "dynamic" {
		t.Fatalf("dynamic-only layout = %+v, want the dynamic block alone", dynOnly)
	}
	if dynOnly[0].CacheControl != "" {
		t.Fatal("dynamic must never acquire a breakpoint, even as the only block")
	}

	if BuildSystemBlocks("", "", "", "") != nil {
		t.Fatal("no content must produce no blocks")
	}
}

// The prefix runs through the LAST marker, so an unmarked block between two
// marked ones is cached — grouping by "carries a marker" would misfile it and
// make detectCacheBreak blame the dynamic tail for a frozen-prefix change.
func TestCachedPrefixIncludesUnmarkedMiddleBlock(t *testing.T) {
	t.Parallel()

	blocks := []agentcore.SystemBlock{
		{Text: "identity", CacheControl: "ephemeral"},
		{Text: "instructions"},
		{Text: "git", CacheControl: "ephemeral"},
		{Text: "dynamic"},
	}
	changed := []agentcore.SystemBlock{
		{Text: "identity", CacheControl: "ephemeral"},
		{Text: "instructions CHANGED"},
		{Text: "git", CacheControl: "ephemeral"},
		{Text: "dynamic"},
	}

	if hashFrozenBlocks(blocks) == hashFrozenBlocks(changed) {
		t.Fatal("unmarked middle block must count toward the frozen fingerprint")
	}
	if hashDynamicBlock(blocks) != hashDynamicBlock(changed) {
		t.Fatal("unmarked middle block must NOT count toward the dynamic fingerprint")
	}
}
