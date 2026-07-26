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
// Lock order: mu → {agent lock, reminders.mu, cache.mu, catalog/tracker
// locks} — all leaves that never call back into this package. Methods never
// reference Session; the one mutable cross-group input (cwd) is passed in.
type promptState struct {
	mu sync.Mutex

	sink      promptSink
	reminders *reminderState      // static-reminder delivery target
	cache     *cacheMonitor       // prompt-fingerprint delivery target
	usage     *skill.UsageTracker // immutable dep; nil → fall back to invocation counts

	frozenIdentity     string // system block 1 — process-stable, never recomputed
	frozenInstructions string // system block 2 — process-stable, never recomputed

	allTools     []agentcore.Tool
	activeTools  []agentcore.Tool
	contextFiles config.ContextFiles
	skills       []skill.Spec
	skillCatalog *skill.Catalog
	overlays     overlayStore
	dynamicText  string // block 3 — refreshed by rebuildLocked; read by teammate spawn

	deferredToolsPreamble string // <available-deferred-tools> for first user message
	preambleInjected      bool   // true after first preamble injection

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

func (p *promptState) setTools(cwd string, tools ...agentcore.Tool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeTools = tools
	p.sink.SetTools(tools...)
	p.rebuildLocked(cwd)
}

func (p *promptState) restoreAllTools(cwd string, extra ...agentcore.Tool) {
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
	p.rebuildLocked(cwd)
}

func (p *promptState) replaceMCPTools(cwd string, tools []agentcore.Tool) {
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
	p.rebuildLocked(cwd)
}

func (p *promptState) setOverlay(cwd, key, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.overlays.set(key, text)
	p.rebuildLocked(cwd)
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
	p.rebuildLocked(cwd)
}

// installReload swaps in freshly loaded context files and skills — the disk
// I/O happens in the caller BEFORE the lock (load-then-swap), so a slow
// filesystem never stalls prompt delivery. The live GitSnapshot is preserved.
func (p *promptState) installReload(cwd string, files config.ContextFiles, skills []skill.Spec) {
	p.mu.Lock()
	defer p.mu.Unlock()
	files.GitSnapshot = p.contextFiles.GitSnapshot
	p.contextFiles = files
	p.skills = skills
	p.rebuildLocked(cwd)
}

func (p *promptState) rebuildLocked(cwd string) {
	if p.skillCatalog != nil {
		p.skills = p.skillCatalog.List()
	}

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

	// Three-block layout for cache stability:
	//   block 1 (identity):     ephemeral, frozen for the process
	//   block 2 (instructions): ephemeral, frozen for the process
	//   block 3 (dynamic):      no cache_control — changes on plan-toggle /
	//                           MCP refresh; a breakpoint here would charge
	//                           1.25x for writes that get invalidated soon.
	blocks := []agentcore.SystemBlock{
		{Text: p.frozenIdentity, CacheControl: "ephemeral"},
		{Text: p.frozenInstructions, CacheControl: "ephemeral"},
	}
	if p.dynamicText != "" {
		blocks = append(blocks, agentcore.SystemBlock{Text: p.dynamicText})
	}
	if p.contextFiles.GitSnapshot != "" {
		blocks = append(blocks, agentcore.SystemBlock{Text: p.contextFiles.GitSnapshot})
	}
	p.sink.SetSystemBlocks(blocks)

	// The preamble is delivered as the first user message — see takePreamble.
	if len(deferredNames) > 0 {
		p.deferredToolsPreamble = "<available-deferred-tools>\n" + strings.Join(deferredNames, "\n") + "\n</available-deferred-tools>"
	} else {
		p.deferredToolsPreamble = ""
	}

	// Static reminders ride along with every user prompt.
	orderedSkills := skill.OrderForPrompt(p.skills, cwd, p.usageScoresLocked())
	p.reminders.setStatic(config.BuildReminders(p.contextFiles, orderedSkills))

	// Refresh cache-break fingerprints. Frozen and dynamic hash separately so
	// detectCacheBreak can pinpoint which block churned. The monitor guards
	// itself and leaves the observed cache_read alone — see updateInputHashes.
	p.cache.updateInputHashes(
		hashFrozenBlocks(blocks),
		hashDynamicBlock(blocks),
		hashTools(p.activeTools),
	)
}

func (p *promptState) refreshSkillReminders(cwd string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.skillCatalog != nil {
		p.skills = p.skillCatalog.List()
	}
	orderedSkills := skill.OrderForPrompt(p.skills, cwd, p.usageScoresLocked())
	p.reminders.setStatic(config.BuildReminders(p.contextFiles, orderedSkills))
}

func (p *promptState) usageScoresLocked() map[string]float64 {
	if p.usage != nil {
		return p.usage.Scores(time.Now())
	}
	return invocationUsageScores(p.invocationCount)
}

// takePreamble consumes the one-shot deferred-tools preamble (Prompt path).
// preambleSnapshot below is the read-only sibling for compaction recovery,
// which must re-inject without consuming.
func (p *promptState) takePreamble() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.preambleInjected || p.deferredToolsPreamble == "" {
		return "", false
	}
	p.preambleInjected = true
	return p.deferredToolsPreamble, true
}

func (p *promptState) preambleSnapshot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deferredToolsPreamble
}

// setPreambleInjected rearms (false) or suppresses (true) preamble injection —
// reset/clear rearm it; a session switch derives it from whether the restored
// history already carries the preamble.
func (p *promptState) setPreambleInjected(injected bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preambleInjected = injected
}

// recordInvoked appends to the invocation log; the usage tracker write (own
// lock, disk-backed) runs after the lock is released, and the caller then
// refreshes skill reminders — sequential locks, never nested, so this path
// cannot self-deadlock the way a rebuild-inside-record would.
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
