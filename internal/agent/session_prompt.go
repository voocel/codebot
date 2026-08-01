package agent

import (
	"path/filepath"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/skill"
	localtools "github.com/voocel/codebot/internal/tools"
)

func (s *Session) ToolsByName(names ...string) []agentcore.Tool {
	return s.prompt.toolsByName(names...)
}

func (s *Session) SetTools(tools ...agentcore.Tool) {
	s.prompt.setTools(tools...)
}

func (s *Session) RestoreAllTools(extra ...agentcore.Tool) {
	s.prompt.restoreAllTools(extra...)
}

// ToolOutputDir resolves where this session persists oversized tool output.
// Resolved per call — the session directory moves on /new and /resume.
func (s *Session) ToolOutputDir() string {
	if s.deps.toolOutputRoot == "" {
		return ""
	}
	return filepath.Join(s.deps.toolOutputRoot, s.SessionID(), localtools.ToolOutputsSubdir)
}

// ReplaceMCPTools installs the current MCP toolset. Output limiting needs no
// wiring here: it runs as middleware around every tool the agent executes, so
// tools registered on a refresh are covered the moment they are called.
func (s *Session) ReplaceMCPTools(tools []agentcore.Tool) {
	s.prompt.replaceMCPTools(tools)
}

func (s *Session) SetMCPInstructions(text string) {
	s.prompt.setOverlay("mcp", text)
}

// OverlayPrompt registers or removes a named instructions overlay.
// Pass empty text to remove. Overlays are rendered sorted by key.
func (s *Session) OverlayPrompt(key, text string) {
	s.prompt.setOverlay(key, text)
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
	files, skills := s.loadWorkspaceContext(cwd)
	s.prompt.installReload(cwd, files, skills)
}

// loadWorkspaceContext reads everything the prompt derives from the workspace
// root. Kept outside the prompt lock so a slow filesystem never stalls
// delivery — both callers swap the result in afterwards.
func (s *Session) loadWorkspaceContext(cwd string) (config.ContextFiles, []skill.Spec) {
	files := config.LoadContextFiles(cwd)
	// Re-read memory from disk (LLM may have updated it during this session).
	files.Memory, files.MemoryDir = config.LoadMemory(cwd)

	var skills []skill.Spec
	if catalog := s.prompt.catalogSnapshot(); catalog != nil {
		// Retarget before List: the catalog decides which skills apply by
		// resolving Spec.Paths against its own cwd, which a worktree switch
		// moves. On a plain reload this is a no-op.
		catalog.Retarget(cwd)
		catalog.Reload()
		skills = catalog.List()
	} else {
		skills = skill.NewCatalog(cwd, nil).List()
	}
	return files, skills
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
