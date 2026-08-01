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
// The returned Watcher records which memory files the run touched; hand it to
// dream.Config so the completion notice can name them. Returned rather than
// accepted so there is no way to wire a different instance into each half and
// silently observe nothing.
func BuildAgentConfig(cwd string, model agentcore.ChatModel, ctxFactory func(agentcore.ChatModel) agentcore.ContextManager, sessionID string) (subagent.Config, *Watcher) {
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
	// The auto-memory rules ride in the system prompt, as they do for the main
	// agent. The consolidation task then points at them instead of restating a
	// subset — and without them dream never sees the "what NOT to save" list,
	// so it happily persists what every other path is told to reject.
	systemPrompt := dreamSystemPrompt
	if rules := config.BuildAutoMemoryInstructions(memDir); rules != "" {
		systemPrompt += "\n\n" + rules
	}
	watcher := NewWatcher()
	return subagent.Config{
		Name:                  agentName,
		Description:           "Background auto-memory consolidation",
		Model:                 model,
		SystemPrompt:          systemPrompt,
		OnMessage:             func(_, _ string, msg agentcore.AgentMessage) { watcher.observe(msg) },
		Tools:                 toolset,
		MaxTurns:              agentMaxTurns,
		ContextManagerFactory: ctxFactory,
		ConvertToLLM:          agentctx.ContextConvertToLLM,
		CacheLastMessage:      "ephemeral",
		PromptCacheKey:        cacheKey,
	}, watcher
}
