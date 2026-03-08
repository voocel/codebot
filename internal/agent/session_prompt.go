package agent

import (
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
		combined := make([]agentcore.Tool, len(m.session.allTools), len(m.session.allTools)+len(extra))
		copy(combined, m.session.allTools)
		m.session.activeTools = append(combined, extra...)
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
	m.session.contextFiles = config.LoadContextFiles(m.session.cwd)
	m.session.skills = config.LoadSkills(m.session.cwd)
	m.rebuildPrompt()
}

func (m *sessionPromptManager) rebuildPrompt() {
	infos := make([]config.ToolInfo, len(m.session.activeTools))
	for i, t := range m.session.activeTools {
		infos[i] = config.ToolInfo{Name: t.Name(), Description: t.Description()}
	}
	base := config.BuildSystemPrompt(m.session.cwd, m.session.contextFiles, infos, m.session.skills)
	if m.session.suffix != "" {
		base += "\n\n" + m.session.suffix
	}
	m.session.agent.SetSystemPrompt(base)
}
