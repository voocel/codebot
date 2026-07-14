package dream

import (
	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/tools"

	"github.com/voocel/codebot/internal/config"
)

const (
	agentName     = "dream"
	agentMaxTurns = 25
)

// BuildAgentConfig assembles the restricted dream agent. It deliberately
// bypasses AgentDefinition/BuildToolPool: the definition registry would
// expose dream to the main model's subagent tool, and the shared pool
// cannot interpose the path guard. The subagent loop has no approval gate,
// so the tool set IS the security boundary: read-only exploration plus
// write/edit confined to the memory directory, and no bash at all.
func BuildAgentConfig(cwd string, model agentcore.ChatModel, ctxFactory func(agentcore.ChatModel) agentcore.ContextManager, sessionID string) subagent.Config {
	memDir := config.MemoryDir(cwd)
	// Private read state, shared between read and write/edit so the
	// read-before-write validation works without touching the main
	// agent's stamps.
	state := tools.NewFileReadState()
	toolset := []agentcore.Tool{
		tools.NewRead(cwd, state),
		tools.NewGlob(cwd),
		tools.NewGrep(cwd),
		tools.NewLs(cwd),
		guardPath(tools.NewWrite(memDir, state), memDir),
		guardPath(tools.NewEdit(memDir, state), memDir),
	}

	cacheKey := ""
	if sessionID != "" {
		cacheKey = sessionID + "-" + agentName
	}
	return subagent.Config{
		Name:                  agentName,
		Description:           "Background auto-memory consolidation",
		Model:                 model,
		SystemPrompt:          dreamSystemPrompt,
		Tools:                 toolset,
		MaxTurns:              agentMaxTurns,
		ContextManagerFactory: ctxFactory,
		ConvertToLLM:          agentctx.ContextConvertToLLM,
		CacheLastMessage:      "ephemeral",
		PromptCacheKey:        cacheKey,
	}
}
