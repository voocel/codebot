package agent

import (
	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/skill"
)

func (s *Session) ToolsByName(names ...string) []agentcore.Tool {
	return s.prompt.toolsByName(names...)
}

func (s *Session) SetTools(tools ...agentcore.Tool) {
	s.prompt.setTools(s.currentCwd(), tools...)
}

func (s *Session) RestoreAllTools(extra ...agentcore.Tool) {
	s.prompt.restoreAllTools(s.currentCwd(), extra...)
}

func (s *Session) ReplaceMCPTools(tools []agentcore.Tool) {
	s.prompt.replaceMCPTools(s.currentCwd(), tools)
}

func (s *Session) SetMCPInstructions(text string) {
	s.prompt.setOverlay(s.currentCwd(), "mcp", text)
}

// OverlayPrompt registers or removes a named instructions overlay.
// Pass empty text to remove. Overlays are rendered sorted by key.
func (s *Session) OverlayPrompt(key, text string) {
	s.prompt.setOverlay(s.currentCwd(), key, text)
}

func (s *Session) Skills() []skill.Spec {
	return s.prompt.skillsSnapshot()
}

func (s *Session) SkillCatalog() *skill.Catalog {
	return s.prompt.catalogSnapshot()
}

// SetSkillCatalog is called from the UI goroutine on plugin reload; the
// catalog install and the prompt rebuild share one critical section.
func (s *Session) SetSkillCatalog(catalog *skill.Catalog) {
	s.prompt.setCatalog(s.currentCwd(), catalog)
}

// Reload re-reads context files, memory, and skills from disk, then swaps
// them in. The I/O runs before the prompt lock (load-then-swap).
func (s *Session) Reload() {
	cwd := s.currentCwd()
	files := config.LoadContextFiles(cwd)
	// Re-read memory from disk (LLM may have updated it during this session).
	files.Memory, files.MemoryDir = config.LoadMemory(cwd)

	var skills []skill.Spec
	if catalog := s.prompt.catalogSnapshot(); catalog != nil {
		catalog.Reload()
		skills = catalog.List()
	} else {
		skills = skill.NewCatalog(cwd, nil).List()
	}
	s.prompt.installReload(cwd, files, skills)
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
