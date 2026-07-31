package agent

import (
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/skill"
)

// promptSink is the narrow slice of the agent promptState delivers to. Both
// methods carry agentcore's documented purity contract (see SetModel's godoc:
// pure assignment under the agent's leaf lock, no events, no callbacks), safe
// to call inside promptState.mu.
type promptSink interface {
	SetTools(tools ...agentcore.Tool)
	SetSystemBlocks(blocks []agentcore.SystemBlock)
}

// promptState owns the tool surface and every input that composes the system
// prompt, plus the skill invocation log that feeds prompt ordering. One lock
// covers compute AND delivery: computing under the lock but delivering
// outside would let two rebuilds interleave (A-compute B-compute B-deliver
// A-deliver), leaving the agent's tools/system permanently out of step with
// the state here.
//
// Lock order: mu → {agent lock, cache.mu, catalog/tracker
// locks} — all leaves that never call back into this package. Methods never
// reference Session; the one mutable cross-group input (cwd) is passed in.
type promptState struct {
	mu sync.Mutex

	sink  promptSink
	cache *cacheMonitor       // prompt-fingerprint delivery target
	usage *skill.UsageTracker // immutable dep; nil → fall back to invocation counts

	// frozenIdentity is system block 1. Stable for the whole session except on
	// a worktree enter/exit, which moves the working directory it states.
	frozenIdentity string

	// frozenInstructions is system block 2. It carries the leader role, the
	// session-stable tool inventory, and the project context (skills,
	// AGENTS.md, MEMORY.md). Recomputed ONLY by rebuildFrozenLocked, i.e. on
	// an explicit reload or catalog swap — never on the plan-mode/MCP paths,
	// which would churn the cached prefix for nothing.
	frozenInstructions string
	// localToolInfos is the session-stable local tool set as the frozen block
	// renders it. Held so a reload can rebuild block 2 without re-deriving it
	// from activeTools, which plan mode filters.
	localToolInfos []config.ToolInfo

	allTools     []agentcore.Tool
	activeTools  []agentcore.Tool
	contextFiles config.ContextFiles
	skills       []skill.Spec
	skillCatalog *skill.Catalog
	overlays     overlayStore
	dynamicText  string // block 3 — refreshed by rebuildLocked; read by teammate spawn

	deferredToolsPreamble string // <available-deferred-tools> for first user message

	invoked         []invokedSkillSnapshot
	invocationCount map[string]int
}

func (p *promptState) toolsByName(names ...string) []agentcore.Tool {
	p.mu.Lock()
	defer p.mu.Unlock()
	allowed := make(map[string]struct{}, len(names))
	for _, n := range names {
		allowed[n] = struct{}{}
	}
	var result []agentcore.Tool
	for _, t := range p.allTools {
		if _, ok := allowed[t.Name()]; ok {
			result = append(result, t)
		}
	}
	return result
}

func (p *promptState) setTools(tools ...agentcore.Tool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeTools = tools
	p.sink.SetTools(tools...)
	p.rebuildLocked()
}

func (p *promptState) restoreAllTools(extra ...agentcore.Tool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(extra) == 0 {
		p.activeTools = p.allTools
	} else {
		existing := make(map[string]struct{}, len(p.allTools))
		for _, t := range p.allTools {
			existing[t.Name()] = struct{}{}
		}
		combined := make([]agentcore.Tool, len(p.allTools), len(p.allTools)+len(extra))
		copy(combined, p.allTools)
		for _, t := range extra {
			if _, dup := existing[t.Name()]; !dup {
				combined = append(combined, t)
			}
		}
		p.activeTools = combined
	}
	p.sink.SetTools(p.activeTools...)
	p.rebuildLocked()
}

func (p *promptState) replaceMCPTools(tools []agentcore.Tool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	wasAllTools := sameToolSet(p.activeTools, p.allTools)
	activeHasMCP := hasMCPTools(p.activeTools)

	p.allTools = replaceMCPToolsInSlice(p.allTools, tools)

	switch {
	case wasAllTools:
		p.activeTools = p.allTools
	case activeHasMCP:
		p.activeTools = replaceMCPToolsInSlice(p.activeTools, tools)
	default:
		return
	}

	p.sink.SetTools(p.activeTools...)
	p.rebuildLocked()
}

func (p *promptState) setOverlay(key, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.overlays.set(key, text)
	p.rebuildLocked()
}

func (p *promptState) setCatalog(cwd string, catalog *skill.Catalog) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.skillCatalog = catalog
	if catalog != nil {
		p.skills = catalog.List()
	} else {
		p.skills = nil
	}
	p.rebuildFrozenLocked(cwd)
	p.rebuildLocked()
}

// installReload swaps in freshly loaded context files and skills — the disk
// I/O happens in the caller BEFORE the lock (load-then-swap), so a slow
// filesystem never stalls prompt delivery. The live GitSnapshot is preserved.
func (p *promptState) installReload(cwd string, files config.ContextFiles, skills []skill.Spec) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.installLocked(cwd, files, skills)
}

// installRetarget is installReload plus block 1, which states the workspace
// root a worktree enter/exit just moved. Costs a cache write; changing
// directories is supposed to.
func (p *promptState) installRetarget(cwd string, files config.ContextFiles, skills []skill.Spec) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frozenIdentity = config.BuildIdentity(cwd, files)
	p.installLocked(cwd, files, skills)
}

func (p *promptState) installLocked(cwd string, files config.ContextFiles, skills []skill.Spec) {
	files.GitSnapshot = p.contextFiles.GitSnapshot
	p.contextFiles = files
	p.skills = skills
	p.rebuildFrozenLocked(cwd)
	p.rebuildLocked()
}

// rebuildFrozenLocked recomputes system block 2. Callers must follow it with
// rebuildLocked to deliver.
//
// Separate from rebuildLocked on purpose: block 2 moves only on an explicit
// reload, while rebuildLocked runs on every plan-mode toggle and MCP refresh.
// Merging them would make those frequent paths pay for a skill-catalog glob
// and risk byte drift in the cached prefix.
func (p *promptState) rebuildFrozenLocked(cwd string) {
	if p.skillCatalog != nil {
		p.skills = p.skillCatalog.List()
	}
	ordered := skill.OrderForPrompt(p.skills, cwd, p.usageScoresLocked())
	p.frozenInstructions = config.BuildLeaderInstructions(p.contextFiles, p.localToolInfos, ordered)
}

// BuildSystemBlocks orders the system prompt from most stable to least. The
// cache is a strict prefix, so position decides whether a block can be cached
// at all — gitSnapshot never changes but would be uncacheable parked after the
// volatile dynamic block.
//
// Breakpoints go on the FIRST and LAST static block, not every one: the prefix
// runs through the final marker, so the middle rides along for free, while the
// one on identity survives a reload that rewrites instructions.
func BuildSystemBlocks(identity, instructions, gitSnapshot, dynamic string) []agentcore.SystemBlock {
	blocks := make([]agentcore.SystemBlock, 0, 4)
	for _, text := range []string{identity, instructions, gitSnapshot} {
		if text != "" {
			blocks = append(blocks, agentcore.SystemBlock{Text: text})
		}
	}
	if n := len(blocks); n > 0 {
		blocks[0].CacheControl = "ephemeral"
		blocks[n-1].CacheControl = "ephemeral"
	}
	// After the breakpoints, so it can never acquire one.
	if dynamic != "" {
		blocks = append(blocks, agentcore.SystemBlock{Text: dynamic})
	}
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

func (p *promptState) rebuildLocked() {
	// Find DeferFilter (if tool_search is active) to exclude deferred tools from prompt.
	var filter agentcore.DeferFilter
	for _, t := range p.activeTools {
		if f, ok := t.(agentcore.DeferFilter); ok {
			filter = f
			break
		}
	}

	// Route active tools to the right system block:
	//   - local tools → already baked into frozenInstructions; nothing to emit here
	//   - MCP tools   → dynamic block (appear/disappear at runtime)
	//   - deferred    → preamble user message, not in system at all
	var mcpInfos []config.ToolInfo
	var deferredNames []string
	for _, t := range p.activeTools {
		name := t.Name()
		if filter != nil && filter.IsDeferred(name) {
			deferredNames = append(deferredNames, name)
			continue
		}
		if config.IsMCPTool(name) {
			mcpInfos = append(mcpInfos, config.ToolInfo{Name: name, Description: t.Description()})
		}
	}

	// dynamicText doubles as the teammate-spawn snapshot (DynamicSystemBlock).
	p.dynamicText = config.BuildDynamicSystemPart(mcpInfos, p.overlays.texts())

	blocks := BuildSystemBlocks(p.frozenIdentity, p.frozenInstructions,
		p.contextFiles.GitSnapshot, p.dynamicText)
	p.sink.SetSystemBlocks(blocks)

	// The preamble is delivered as the first user message — see
	// Session.pendingPreamble, which decides delivery from the transcript.
	if len(deferredNames) > 0 {
		p.deferredToolsPreamble = "<available-deferred-tools>\n" + strings.Join(deferredNames, "\n") + "\n</available-deferred-tools>"
	} else {
		p.deferredToolsPreamble = ""
	}

	// Refresh cache-break fingerprints. Frozen and dynamic hash separately so
	// detectCacheBreak can pinpoint which block churned. The monitor guards
	// itself and leaves the observed cache_read alone — see updateInputHashes.
	p.cache.updateInputHashes(
		hashFrozenBlocks(blocks),
		hashDynamicBlock(blocks),
		hashTools(p.activeTools),
	)
}

// usageScoresLocked ranks skills for RenderListing's budget selection. The
// scores decay with wall time, which is why they must never reach the rendered
// ORDER — see skill.RenderListing.
func (p *promptState) usageScoresLocked() map[string]float64 {
	if p.usage != nil {
		return p.usage.Scores(time.Now())
	}
	return invocationUsageScores(p.invocationCount)
}

func (p *promptState) preambleSnapshot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deferredToolsPreamble
}

// recordInvoked appends to the invocation log. The usage tracker write (own
// lock, disk-backed) runs after this lock is released — sequential, never
// nested, so this path cannot self-deadlock the way a rebuild-inside-record
// would. No prompt rebuild follows: usage only decides which skills survive
// the listing budget, never their rendered order.
func (p *promptState) recordInvoked(snapshot invokedSkillSnapshot, usageName string) error {
	p.mu.Lock()
	if p.invocationCount == nil {
		p.invocationCount = make(map[string]int)
	}
	p.invocationCount[usageName]++
	list := append(p.invoked, snapshot)
	if len(list) > 4 {
		list = append([]invokedSkillSnapshot(nil), list[len(list)-4:]...)
	}
	p.invoked = list
	p.mu.Unlock()

	if p.usage != nil {
		return p.usage.Record(usageName, snapshot.Timestamp)
	}
	return nil
}

func (p *promptState) invokedSnapshot() []invokedSkillSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]invokedSkillSnapshot(nil), p.invoked...)
}

func (p *promptState) resetInvocations() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.invoked = nil
	p.invocationCount = nil
}

// Snapshot accessors. Tool slices are returned as-is: writers always install
// fresh slices (never mutate in place), so a returned slice is immutable.

func (p *promptState) activeToolsSnapshot() []agentcore.Tool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeTools
}

func (p *promptState) allToolsSnapshot() []agentcore.Tool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allTools
}

func (p *promptState) skillsSnapshot() []skill.Spec {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Detached copy: the caller (plugin reload UI) iterates while a rebuild
	// may replace the slice concurrently.
	return append([]skill.Spec(nil), p.skills...)
}

func (p *promptState) catalogSnapshot() *skill.Catalog {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.skillCatalog
}

func (p *promptState) identitySnapshot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.frozenIdentity
}

func (p *promptState) dynamicSnapshot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dynamicText
}

func (p *promptState) memoryDir() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.contextFiles.MemoryDir
}
