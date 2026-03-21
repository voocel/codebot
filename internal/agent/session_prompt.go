package agent

import (
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
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

func (s *Session) ReplaceAllTools(tools []agentcore.Tool) {
	s.prompts.replaceAllTools(tools)
}

func (s *Session) SetSystemSuffix(suffix string) {
	s.prompts.setSystemSuffix(suffix)
}

func (s *Session) Skills() []config.Skill {
	return s.skills
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

func (m *sessionPromptManager) replaceAllTools(tools []agentcore.Tool) {
	m.session.allTools = tools
	m.session.activeTools = tools
	m.session.agent.SetTools(tools...)
	m.rebuildPrompt()
}

func (m *sessionPromptManager) setSystemSuffix(suffix string) {
	m.session.suffix = suffix
	m.rebuildPrompt()
}

func (m *sessionPromptManager) reload() {
	gitSnapshot := m.session.contextFiles.GitSnapshot
	m.session.contextFiles = config.LoadContextFiles(m.session.cwd)
	m.session.contextFiles.GitSnapshot = gitSnapshot
	// Re-read memory from disk (LLM may have updated it during this session).
	m.session.contextFiles.Memory, m.session.contextFiles.MemoryDir = config.LoadMemory(m.session.cwd)
	m.session.skills = config.LoadSkills(m.session.cwd)
	m.rebuildPrompt()
}

func (m *sessionPromptManager) rebuildPrompt() {
	// Find DeferFilter (if tool_search is active) to exclude deferred tools from prompt.
	var filter agentcore.DeferFilter
	for _, t := range m.session.activeTools {
		if f, ok := t.(agentcore.DeferFilter); ok {
			filter = f
			break
		}
	}

	var visibleInfos []config.ToolInfo
	var deferredNames []string
	for _, t := range m.session.activeTools {
		if filter != nil && filter.IsDeferred(t.Name()) {
			deferredNames = append(deferredNames, t.Name())
		} else {
			visibleInfos = append(visibleInfos, config.ToolInfo{Name: t.Name(), Description: t.Description()})
		}
	}

	// Build two-block system prompt (identity + instructions) for cache stability.
	identity, instructions := config.BuildSystemBlockTexts(m.session.cwd, m.session.contextFiles, visibleInfos)
	if m.session.suffix != "" {
		instructions += "\n\n" + m.session.suffix
	}
	blocks := []agentcore.SystemBlock{
		{Text: identity, CacheControl: "ephemeral"},
		{Text: instructions, CacheControl: "ephemeral"},
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
	m.session.reminders = config.BuildReminders(m.session.contextFiles, m.session.skills)
	m.session.mu.Unlock()
}
