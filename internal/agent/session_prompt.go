package agent

import (
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/skill"
)

type sessionPromptManager struct {
	session *Session
}

func newSessionPromptManager(session *Session) *sessionPromptManager {
	return &sessionPromptManager{session: session}
}

func (s *Session) ToolsByName(names ...string) []agentcore.Tool {
	return s.prompts.toolsByName(names...)
}

func (s *Session) SetTools(tools ...agentcore.Tool) {
	s.prompts.setTools(tools...)
}

func (s *Session) RestoreAllTools(extra ...agentcore.Tool) {
	s.prompts.restoreAllTools(extra...)
}

func (s *Session) ReplaceMCPTools(tools []agentcore.Tool) {
	s.prompts.replaceMCPTools(tools)
}

func (s *Session) SetMCPInstructions(text string) {
	s.prompts.overlayPrompt("mcp", text)
}

// OverlayPrompt registers or removes a named instructions overlay.
// Pass empty text to remove. Overlays are rendered in insertion order.
func (s *Session) OverlayPrompt(key, text string) {
	s.prompts.overlayPrompt(key, text)
}

func (s *Session) Skills() []skill.Spec {
	return s.skills
}

func (s *Session) SkillCatalog() *skill.Catalog {
	return s.skillCatalog
}

func (s *Session) SetSkillCatalog(catalog *skill.Catalog) {
	s.skillCatalog = catalog
	if catalog != nil {
		s.skills = catalog.List()
	} else {
		s.skills = nil
	}
	if s.prompts != nil {
		s.prompts.rebuildPrompt()
	}
}

func (s *Session) Reload() {
	s.prompts.reload()
}

func (m *sessionPromptManager) toolsByName(names ...string) []agentcore.Tool {
	allowed := make(map[string]struct{}, len(names))
	for _, n := range names {
		allowed[n] = struct{}{}
	}
	var result []agentcore.Tool
	for _, t := range m.session.allTools {
		if _, ok := allowed[t.Name()]; ok {
			result = append(result, t)
		}
	}
	return result
}

func (m *sessionPromptManager) setTools(tools ...agentcore.Tool) {
	m.session.activeTools = tools
	m.session.agent.SetTools(tools...)
	m.rebuildPrompt()
}

func (m *sessionPromptManager) restoreAllTools(extra ...agentcore.Tool) {
	if len(extra) == 0 {
		m.session.activeTools = m.session.allTools
	} else {
		existing := make(map[string]struct{}, len(m.session.allTools))
		for _, t := range m.session.allTools {
			existing[t.Name()] = struct{}{}
		}
		combined := make([]agentcore.Tool, len(m.session.allTools), len(m.session.allTools)+len(extra))
		copy(combined, m.session.allTools)
		for _, t := range extra {
			if _, dup := existing[t.Name()]; !dup {
				combined = append(combined, t)
			}
		}
		m.session.activeTools = combined
	}
	m.session.agent.SetTools(m.session.activeTools...)
	m.rebuildPrompt()
}

func (m *sessionPromptManager) replaceMCPTools(tools []agentcore.Tool) {
	wasAllTools := sameToolSet(m.session.activeTools, m.session.allTools)
	activeHasMCP := hasMCPTools(m.session.activeTools)

	m.session.allTools = replaceMCPToolsInSlice(m.session.allTools, tools)

	switch {
	case wasAllTools:
		m.session.activeTools = m.session.allTools
	case activeHasMCP:
		m.session.activeTools = replaceMCPToolsInSlice(m.session.activeTools, tools)
	default:
		return
	}

	m.session.agent.SetTools(m.session.activeTools...)
	m.rebuildPrompt()
}

func (m *sessionPromptManager) overlayPrompt(key, text string) {
	m.session.overlays.set(key, text)
	m.rebuildPrompt()
}

func (m *sessionPromptManager) reload() {
	gitSnapshot := m.session.contextFiles.GitSnapshot
	m.session.contextFiles = config.LoadContextFiles(m.session.cwd)
	m.session.contextFiles.GitSnapshot = gitSnapshot
	// Re-read memory from disk (LLM may have updated it during this session).
	m.session.contextFiles.Memory, m.session.contextFiles.MemoryDir = config.LoadMemory(m.session.cwd)
	if m.session.skillCatalog != nil {
		m.session.skillCatalog.Reload()
		m.session.skills = m.session.skillCatalog.List()
	} else {
		m.session.skills = skill.NewCatalog(m.session.cwd, nil).List()
	}
	m.rebuildPrompt()
}

func (m *sessionPromptManager) rebuildPrompt() {
	if m.session.skillCatalog != nil {
		m.session.skills = m.session.skillCatalog.List()
	}

	// Find DeferFilter (if tool_search is active) to exclude deferred tools from prompt.
	var filter agentcore.DeferFilter
	for _, t := range m.session.activeTools {
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
	for _, t := range m.session.activeTools {
		name := t.Name()
		if filter != nil && filter.IsDeferred(name) {
			deferredNames = append(deferredNames, name)
			continue
		}
		if config.IsMCPTool(name) {
			mcpInfos = append(mcpInfos, config.ToolInfo{Name: name, Description: t.Description()})
		}
	}

	dynamicText := config.BuildDynamicSystemPart(mcpInfos, m.session.overlays.texts())
	// Mirror the current dynamic text onto the Session so teammate spawn can
	// read it via DynamicSystemBlock() without re-running the builder. mu is
	// also taken below for cacheSnap; merging into that critical section
	// would tighten the lock window but the assignment is a single
	// reference-store, so cost is negligible.
	m.session.mu.Lock()
	m.session.dynamicText = dynamicText
	m.session.mu.Unlock()

	// Three-block layout for cache stability:
	//   block 1 (identity):     ephemeral, frozen for the process
	//   block 2 (instructions): ephemeral, frozen for the process
	//   block 3 (dynamic):      no cache_control — changes on plan-toggle /
	//                           MCP refresh; a breakpoint here would charge
	//                           1.25x for writes that get invalidated soon.
	blocks := []agentcore.SystemBlock{
		{Text: m.session.frozenIdentity, CacheControl: "ephemeral"},
		{Text: m.session.frozenInstructions, CacheControl: "ephemeral"},
	}
	if dynamicText != "" {
		blocks = append(blocks, agentcore.SystemBlock{Text: dynamicText})
	}
	if m.session.contextFiles.GitSnapshot != "" {
		blocks = append(blocks, agentcore.SystemBlock{Text: m.session.contextFiles.GitSnapshot})
	}
	m.session.agent.SetSystemBlocks(blocks)

	// Update deferred tools preamble (injected as first user message).
	if len(deferredNames) > 0 {
		m.session.deferredToolsPreamble = "<available-deferred-tools>\n" + strings.Join(deferredNames, "\n") + "\n</available-deferred-tools>"
	} else {
		m.session.deferredToolsPreamble = ""
	}

	// Update reminders (skills + context files, injected per user message).
	m.session.mu.Lock()
	orderedSkills := skill.OrderForPrompt(m.session.skills, m.session.cwd, m.session.skillUsageScoresLocked())
	m.session.reminders.static = config.BuildReminders(m.session.contextFiles, orderedSkills)
	// Refresh cache-break fingerprints. CacheReadTokens / Valid are owned by
	// persistLLMCall — only the input hashes are updated here, so a prompt
	// rebuild mid-session leaves the "previous observed cache_read" intact
	// and the next turn can still detect a drop. Frozen and dynamic hash
	// separately so detectCacheBreak can pinpoint which block churned.
	m.session.cacheSnap.FrozenSystemHash = hashFrozenBlocks(blocks)
	m.session.cacheSnap.DynamicSystemHash = hashDynamicBlock(blocks)
	m.session.cacheSnap.ToolsHash = hashTools(m.session.activeTools)
	m.session.mu.Unlock()
}

func (m *sessionPromptManager) refreshSkillReminders() {
	m.session.mu.Lock()
	if m.session.skillCatalog != nil {
		m.session.skills = m.session.skillCatalog.List()
	}
	orderedSkills := skill.OrderForPrompt(m.session.skills, m.session.cwd, m.session.skillUsageScoresLocked())
	m.session.reminders.static = config.BuildReminders(m.session.contextFiles, orderedSkills)
	m.session.mu.Unlock()
}

func replaceMCPToolsInSlice(base []agentcore.Tool, mcpTools []agentcore.Tool) []agentcore.Tool {
	out := make([]agentcore.Tool, 0, len(base)+len(mcpTools))
	for _, tool := range base {
		if config.IsMCPTool(tool.Name()) {
			continue
		}
		out = append(out, tool)
	}
	out = append(out, mcpTools...)
	return out
}

func hasMCPTools(tools []agentcore.Tool) bool {
	for _, tool := range tools {
		if config.IsMCPTool(tool.Name()) {
			return true
		}
	}
	return false
}

func sameToolSet(a, b []agentcore.Tool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name() != b[i].Name() {
			return false
		}
	}
	return true
}

func (s *Session) skillUsageScoresLocked() map[string]float64 {
	if s.skillUsage != nil {
		return s.skillUsage.Scores(time.Now())
	}
	return invocationUsageScores(s.skillRuntime.invocationCount)
}
