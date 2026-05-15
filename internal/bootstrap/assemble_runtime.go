package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	localtools "github.com/voocel/codebot/internal/tools"
)

func assembleRuntime(input *resolvedInput, services *bootServices, assembly *sessionAssembly) (*Runtime, error) {
	taskRT := task.NewRuntime()
	taskTools := localtools.NewTaskTools(services.taskStore, taskRT, assembly.hookRunner)
	tools := make([]agentcore.Tool, 0, len(assembly.tools)+len(taskTools))
	tools = append(tools, assembly.tools...)
	tools = append(tools, taskTools...)
	baseTools := make([]agentcore.Tool, 0, len(assembly.baseTools)+len(taskTools))
	baseTools = append(baseTools, assembly.baseTools...)
	baseTools = append(baseTools, taskTools...)

	reserveTokens := 0 // 0 = engine default (fixed buffer)
	if r := assembly.settings.CompactRatio; r > 0 && r < 1 {
		reserveTokens = assembly.settings.ContextWindow - int(float64(assembly.settings.ContextWindow)*r)
	}
	contextEngine, summaryCompact := buildContextEngine(assembly.chatModel, assembly.settings.ContextWindow, reserveTokens, input.cwd)
	agentCore, err := buildAgent(assembly, services, contextEngine, tools)
	if err != nil {
		return nil, err
	}

	if err := restoreAgentState(input, assembly, tools, agentCore); err != nil {
		return nil, err
	}

	session := buildSession(input, services, assembly, contextEngine, agentCore, tools)
	wireSessionRuntime(input, assembly, services, session, baseTools, tools, agentCore, taskRT, contextEngine, summaryCompact)

	modelName := config.FormatModelID(assembly.settings.Provider, assembly.settings.Model)
	if session != nil && session.ModelName() != "" {
		modelName = session.ModelName()
	}

	return &Runtime{
		Cwd:            input.cwd,
		GitBranch:      detectGitBranch(input.cwd),
		ApprovalEngine: services.approvalEngine,
		TaskRuntime:    taskRT,
		Settings:       assembly.settings,
		ModelName:      modelName,
		Session:        session,
		SessionStore:   input.sessionStore,
		PluginCatalog:  services.pluginCatalog,
		SkillCatalog:   services.skillCatalog,
		MCPManager:     services.mcpManager,
		MCPServers:     services.mcpServers,
		HookRunner:     assembly.hookRunner,
		EnvHint:        input.envHint,
		PlanSlug:       input.sessionSnapshot.PlanSlug,
		PlanPhase:      input.sessionSnapshot.PlanPhase,
		PlanPreMode:    input.sessionSnapshot.PlanPreMode,
	}, nil
}

func buildContextEngine(chatModel agentcore.ChatModel, contextWindow, reserveTokens int, cwd string) (*agentctx.ContextEngine, *agentctx.FullSummaryStrategy) {
	toolCompact := agentctx.NewToolResultMicrocompact(agentctx.ToolResultMicrocompactConfig{
		Classifier: agent.CodebotToolClassifier,
		KeepRecent: 5,
	})
	trimCompact := agentctx.NewLightTrim(agentctx.LightTrimConfig{})
	// SessionMemory-backed compaction runs ahead of the LLM summary path so
	// that a populated session-memory.md reuses the living document instead of
	// triggering a synchronous summarization call. Empty / template-only
	// memory files fall through to FullSummary automatically.
	memoryCompact := agentctx.NewSessionMemory(agentctx.SessionMemoryConfig{
		SeedFn: agent.SessionMemorySeedFn(cwd),
	})
	summaryCompact := agentctx.NewFullSummary(agentctx.FullSummaryConfig{
		Model: chatModel,
	})
	engine := agentctx.NewEngine(agentctx.EngineConfig{
		ContextWindow: contextWindow,
		ReserveTokens: reserveTokens,
		Strategies: []agentctx.Strategy{
			toolCompact,
			trimCompact,
			memoryCompact,
			summaryCompact,
		},
	})
	return engine, summaryCompact
}

func buildAgent(assembly *sessionAssembly, services *bootServices, contextEngine agentcore.ContextManager, tools []agentcore.Tool) (*agentcore.Agent, error) {
	opts := []agentcore.AgentOption{
		agentcore.WithModel(assembly.chatModel),
		agentcore.WithSystemBlocks(assembly.systemBlocks),
		agentcore.WithTools(tools...),
		agentcore.WithMaxTurns(assembly.settings.MaxTurns),
		agentcore.WithMaxRetries(5),
		agentcore.WithMaxToolErrors(3),
		agentcore.WithMaxToolConcurrency(4),
		agentcore.WithContextManager(contextEngine),
		agentcore.WithToolGate(services.approvalEngine.AsToolGate()),
		// Place the single message-level cache write breakpoint on the freshest
		// non-system turn (user input, tool_result, or assistant). System
		// blocks 1/2 already carry their own cache_control; this breakpoint
		// covers the growing prefix so each LLM call inside a tool loop reads
		// the previous tool_use+tool_result pair from cache instead of
		// re-uploading them.
		agentcore.WithCacheLastMessage("ephemeral"),
	}
	if assembly.hookMiddleware != nil {
		opts = append(opts, agentcore.WithMiddlewares(assembly.hookMiddleware))
	}
	return agentcore.NewAgent(opts...), nil
}

func restoreAgentState(input *resolvedInput, assembly *sessionAssembly, tools []agentcore.Tool, ag *agentcore.Agent) error {
	if len(input.sessionSnapshot.Messages) > 0 {
		if err := ag.SetMessages(input.sessionSnapshot.Messages); err != nil {
			return fmt.Errorf("restore agent messages: %w", err)
		}
		agentcore.ReactivateDeferred(tools, input.sessionSnapshot.Messages)
	}
	if input.sessionSnapshot.Thinking != "" {
		ag.SetThinkingLevel(agentcore.ThinkingLevel(input.sessionSnapshot.Thinking))
		assembly.settings.ThinkingLevel = input.sessionSnapshot.Thinking
	} else if assembly.settings.ThinkingLevel != "" {
		ag.SetThinkingLevel(agentcore.ThinkingLevel(assembly.settings.ThinkingLevel))
	}
	return nil
}

func buildSession(input *resolvedInput, services *bootServices, assembly *sessionAssembly, contextEngine agentcore.ContextManager, ag *agentcore.Agent, tools []agentcore.Tool) *agent.Session {
	return agent.NewSession(agent.SessionConfig{
		Agent:                 ag,
		ContextManager:        contextEngine,
		Store:                 input.sessionStore,
		Manager:               input.sessionManager,
		Registry:              input.registry,
		Settings:              assembly.settings,
		Cwd:                   input.cwd,
		CreateModel:           input.modelFactory,
		ChatModel:             assembly.chatModel,
		Tools:                 tools,
		TaskStore:             services.taskStore,
		ContextFiles:          assembly.contextFiles,
		Skills:                services.skills,
		SkillCatalog:          services.skillCatalog,
		SkillUsage:            services.skillUsage,
		HookRunner:            assembly.hookRunner,
		FrozenIdentity:        assembly.frozenIdentity,
		FrozenInstructions:    assembly.frozenInstructions,
		InitialMCPOverlay:     assembly.initialMCPOverlay,
		DeferredToolsPreamble: assembly.deferredToolsPreamble,
		Reminders:             assembly.reminders,
		PreambleInjected:      len(input.sessionSnapshot.Messages) > 0,
		SkillAllowsSetter:     services.approvalEngine.SetSkillAllows,
	})
}

func wireSessionRuntime(input *resolvedInput, assembly *sessionAssembly, services *bootServices, session *agent.Session, baseTools, tools []agentcore.Tool, ag *agentcore.Agent, taskRT *task.Runtime, contextEngine *agentctx.ContextEngine, summaryCompact *agentctx.FullSummaryStrategy) {
	summaryCompact.SetPostSummaryHooks(session.PostSummaryRecoveryHook())
	contextEngine.SetProjectHook(session.HandleProjectedRewrite)
	contextEngine.SetRecoverHook(session.HandleOverflowRewrite)

	for _, tool := range tools {
		if st, ok := tool.(*localtools.SkillTool); ok {
			st.SetInvocationApplier(session.ApplySkillInvocation)
		}
	}

	assembly.subagentTool.SetTaskRuntime(taskRT)
	assembly.subagentTool.SetNotifyFn(ag.FollowUp)

	sessionID := input.sessionStore.Header().SessionID
	bgDir := filepath.Join(config.SessionsDir(input.cwd), sessionID, "bg")
	assembly.subagentTool.SetBgOutputFactory(func(taskID, agentName string) (io.WriteCloser, string, error) {
		dir := filepath.Join(bgDir, taskID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, "", err
		}
		path := filepath.Join(dir, "output.jsonl")
		f, err := os.Create(path)
		if err != nil {
			return nil, "", err
		}
		meta, _ := json.Marshal(map[string]string{"agent": agentName})
		_ = os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o600)
		return f, path, nil
	})

	if assembly.bashTool != nil {
		assembly.bashTool.SetTaskRuntime(taskRT)
		assembly.bashTool.SetNotifyFn(ag.FollowUp)
		assembly.bashTool.SetBgOutputFactory(func(shellID string) (io.WriteCloser, string, error) {
			dir := filepath.Join(bgDir, shellID)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, "", err
			}
			path := filepath.Join(dir, "output.log")
			f, err := os.Create(path)
			return f, path, err
		})
	}

	if services.mcpManager != nil {
		session.SetBeforePrompt(func() {
			mcpTools, ok := services.mcpManager.RefreshIfDirty(context.Background())
			if !ok {
				return
			}
			all := make([]agentcore.Tool, len(baseTools), len(baseTools)+len(mcpTools))
			copy(all, baseTools)
			all = append(all, mcpTools...)
			session.ReplaceAllTools(all)
		})
	}

	if assembly.hookRunner != nil {
		assembly.hookRunner.RunSessionStart(context.Background())
	}
}

func detectGitBranch(cwd string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
