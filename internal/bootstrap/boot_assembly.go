package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/memory"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/hooks"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/storage"
	localtools "github.com/voocel/codebot/internal/tools"
)

type bootInput struct {
	cwd         string
	settings    config.Resolved
	registry    *provider.ModelRegistry
	profile     approval.Profile
	createModel agent.ModelFactory
	manager     *storage.Manager
	store       *storage.Store
	snapshot    storage.ContextSnapshot
	nonTTY      bool
	envHint     string // non-empty when credentials come from environment variable
}

type bootSpec struct {
	settings              config.Resolved
	activeProvider        string
	activeModel           string
	chatModel             agentcore.ChatModel
	tools                 []agentcore.Tool
	baseTools             []agentcore.Tool
	systemBlocks          []agentcore.SystemBlock
	deferredToolsPreamble string
	reminders             []string
	contextFiles          config.ContextFiles
	skills                []config.Skill
	mcpManager            *mcpclient.Manager
	mcpServers            map[string]mcpclient.ServerConfig
	subagentTool          *agentcore.SubAgentTool
	bashTool              *agentcoretools.BashTool
	toolApproval          agentcore.ToolApprovalFunc
	approvalEngine        *approval.Engine
	hookMiddleware        agentcore.ToolMiddleware // nil = no hooks configured
	hookRunner            *hooks.Runner
}

func resolveBootInput(opts Options) (*bootInput, error) {
	cwd := opts.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get cwd: %w", err)
		}
	}

	settings := config.ResolveAll(cwd)
	registry := provider.NewModelRegistry()
	provider.StartPricingRefresh(registry, config.UserConfigDir())

	profile, err := approval.ParseProfile(opts.ApprovalProfile)
	if err != nil {
		return nil, err
	}

	createModel := opts.ModelFactory
	if createModel == nil {
		createModel = provider.CreateModel
	}

	settings, envHint, err := ensureProviderSetup(cwd, settings, opts.NonTTYMode)
	if err != nil {
		return nil, err
	}

	manager := storage.NewManager(config.SessionsDir(cwd))
	store, err := resolveSession(manager, cwd, opts.Continue, opts.Resume, opts.NonTTYMode)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}

	var snapshot storage.ContextSnapshot
	if opts.Continue || opts.Resume {
		snapshot, err = store.BuildSnapshot()
		if err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("restore context: %w", err)
		}
	}

	return &bootInput{
		cwd:         cwd,
		settings:    settings,
		registry:    registry,
		profile:     profile,
		createModel: createModel,
		manager:     manager,
		store:       store,
		snapshot:    snapshot,
		nonTTY:      opts.NonTTYMode,
		envHint:     envHint,
	}, nil
}

func ensureProviderSetup(cwd string, settings config.Resolved, nonTTY bool) (config.Resolved, string, error) {
	// Configured provider has credentials (config or env).
	apiKey, _ := settings.ProviderCredentials(settings.Provider)
	if apiKey != "" {
		hint := envHintFor(settings)
		return settings, hint, nil
	}

	// Auto-detect: scan all known providers for env var credentials.
	if prov, envKey := config.DetectEnvProvider(); prov != "" {
		settings.Provider = prov
		settings.Model = config.DefaultModelName(prov)
		settings.SmallModel = settings.Model
		hint := fmt.Sprintf("Using %s from environment", envKey)
		return settings, hint, nil
	}

	// No credentials anywhere — run interactive setup.
	if nonTTY {
		return settings, "", fmt.Errorf("api key not set; set %s or configure providers in %s",
			config.ProviderEnvKey(settings.Provider), filepath.Join(config.UserConfigDir(), "settings.json"))
	}

	err := config.RunSetup(settings)
	if err != nil {
		return settings, "", fmt.Errorf("setup: %w", err)
	}
	return config.ResolveAll(cwd), "", nil
}

// envHintFor returns a hint string if the active provider's API key comes from an environment variable.
func envHintFor(settings config.Resolved) string {
	if pc, ok := settings.Providers[settings.Provider]; ok && pc.APIKey != "" {
		return "" // key from config file
	}
	envKey := config.ProviderEnvKey(settings.Provider)
	return fmt.Sprintf("Using %s from environment", envKey)
}

func assembleBootSpec(input *bootInput, factories []ToolFactory) (*bootSpec, error) {
	activeProvider := input.settings.Provider
	if input.snapshot.Provider != "" {
		activeProvider = input.snapshot.Provider
	}
	activeModel := input.settings.Model
	if input.snapshot.Model != "" {
		activeModel = input.snapshot.Model
	}

	activeAPIKey, activeBaseURL := input.settings.ProviderCredentials(activeProvider)
	provType := "openai"
	if pc, ok := input.settings.Providers[activeProvider]; ok {
		provType = pc.ProviderType(activeProvider)
	} else if t, ok := config.KnownProviderTypes[activeProvider]; ok {
		provType = t
	}
	chatModel, err := input.createModel(provType, activeModel, activeAPIKey, activeBaseURL)
	if err != nil {
		return nil, fmt.Errorf("create model: %w", err)
	}

	settings := input.settings
	if settings.ContextWindow <= 0 {
		if entry, _, err := input.registry.Resolve(activeModel); err == nil && entry.ContextWindow > 0 {
			settings.ContextWindow = entry.ContextWindow
		} else {
			settings.ContextWindow = 128000
		}
	}
	settings.Provider = activeProvider
	settings.Model = activeModel

	ctxFiles := config.LoadContextFiles(input.cwd)
	ctxFiles.GitSnapshot = config.CollectGitSnapshot(input.cwd)
	ctxFiles.Memory, ctxFiles.MemoryDir = config.LoadMemory(input.cwd)
	config.EnsureMemoryDir(input.cwd)
	skills := config.LoadSkills(input.cwd)

	rules, err := approval.ParseRuleSet(settings.Permissions.Allow, settings.Permissions.Deny)
	if err != nil {
		return nil, fmt.Errorf("parse permission rules: %w", err)
	}

	approvalEngine, err := approval.NewEngine(input.cwd, input.profile, rules, approvalAuditor(config.AuditLogPath()))
	if err != nil {
		return nil, fmt.Errorf("approval engine: %w", err)
	}

	tools, baseTools, mcpManager, mcpServers, subagentTool, bashTool, err := buildToolset(input, settings, activeProvider, chatModel, factories)
	if err != nil {
		return nil, err
	}
	approvalEngine.ReplaceToolMetadata(tools)

	parts := buildSystemParts(input.cwd, tools, ctxFiles, skills, mcpManager)

	var hookMW agentcore.ToolMiddleware
	var hookRunner *hooks.Runner
	if len(settings.Hooks) > 0 {
		hookRunner = hooks.New(settings.Hooks, input.store.Header().SessionID, approvalEngine)
		if hookRunner != nil {
			hookMW = hookRunner.Middleware()
		}
	}

	return &bootSpec{
		settings:              settings,
		activeProvider:        activeProvider,
		activeModel:           activeModel,
		chatModel:             chatModel,
		tools:                 tools,
		baseTools:             baseTools,
		systemBlocks:          parts.blocks,
		deferredToolsPreamble: parts.deferredMsg,
		reminders:             parts.reminders,
		contextFiles:          ctxFiles,
		skills:                skills,
		mcpManager:            mcpManager,
		mcpServers:            mcpServers,
		subagentTool:          subagentTool,
		bashTool:              bashTool,
		toolApproval:          approvalEngine.ApproveTool,
		approvalEngine:        approvalEngine,
		hookMiddleware:        hookMW,
		hookRunner:            hookRunner,
	}, nil
}

func buildToolset(input *bootInput, settings config.Resolved, activeProvider string, chatModel agentcore.ChatModel, factories []ToolFactory) ([]agentcore.Tool, []agentcore.Tool, *mcpclient.Manager, map[string]mcpclient.ServerConfig, *agentcore.SubAgentTool, *agentcoretools.BashTool, error) {
	builtTools := buildTools(input.cwd, factories)

	// Find the BashTool from built tools for background shell wiring.
	var bashTool *agentcoretools.BashTool
	for _, tool := range builtTools {
		if bt, ok := tool.(*agentcoretools.BashTool); ok {
			bashTool = bt
			break
		}
	}
	askTool := localtools.NewAskUser()
	taskStore, taskTools := localtools.NewTaskTools()
	_, cronTools := localtools.NewCronTools()
	builtTools = append(builtTools,
		localtools.NewWebFetch(settings.SearchProvider, settings.SearchAPIKey),
		localtools.NewWebSearch(settings.SearchProvider, settings.SearchAPIKey),
		askTool,
		localtools.NewEnterPlanMode(),
		localtools.NewExitPlanMode(),
	)
	builtTools = append(builtTools, taskTools...)
	builtTools = append(builtTools, cronTools...)
	builtTools = localtools.WrapWithOutputLimit(builtTools)

	taskDir := filepath.Join(config.TasksDir(), input.store.Header().SessionID)
	if err := taskStore.SetDir(taskDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: task persistence: %v\n", err)
	}

	subagentTool := buildSubAgentTool(subAgentDeps{
		Cwd:         input.cwd,
		Model:       chatModel,
		AllTools:    builtTools,
		CreateModel: input.createModel,
		Provider:    activeProvider,
		Providers:   settings.Providers,
		SmallModel:  settings.SmallModel, // already resolved: provider config > main model
	})
	builtTools = append(builtTools, subagentTool)
	baseTools := builtTools

	var mcpManager *mcpclient.Manager
	mcpServers := mcpclient.LoadAllMCPServers(input.cwd)
	if len(mcpServers) > 0 {
		mcpManager = mcpclient.NewManager()
	}

	builtTools, baseTools = applyToolSearch(builtTools, baseTools, activeProvider, settings.Model)

	return builtTools, baseTools, mcpManager, mcpServers, subagentTool, bashTool, nil
}

// coreToolNames are tools that remain always visible to the LLM.
// When tool search is enabled, all tools except tool_search itself are deferred.
// tool_search is added separately and never appears in the deferred set.
var coreToolNames = map[string]bool{}

// supportsToolSearch reports whether the given provider/model combination
// supports deferred tool search. Currently only Claude models and GPT-5.4+
// are known to handle tool search reliably.
func supportsToolSearch(provider, model string) bool {
	p := strings.ToLower(provider)
	m := strings.ToLower(model)

	// Strip provider prefix (e.g. "anthropic/claude-sonnet-4.6" → "claude-sonnet-4.6")
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}

	// Claude: Sonnet/Opus 4.5+, no Haiku.
	if p == "anthropic" || strings.HasPrefix(m, "claude") {
		if strings.Contains(m, "haiku") {
			return false
		}
		return claudeVersionAtLeast(m, 4, 5)
	}

	// OpenAI: gpt-5.4 and above.
	if p == "openai" || strings.HasPrefix(m, "gpt-") {
		return modelVersionAtLeast(strings.TrimPrefix(m, "gpt-"), 5, 4)
	}

	return false
}

// claudeVersionAtLeast checks if a Claude model name contains a version >= major.minor.
// Handles both new format (claude-sonnet-4-5, claude-opus-4.6)
// and old format (claude-3-5-sonnet, claude-3-opus).
func claudeVersionAtLeast(model string, minMajor, minMinor int) bool {
	m := strings.TrimPrefix(model, "claude-")
	// Strip family prefix for new format: "sonnet-4-5-..." → "4-5-..."
	for _, family := range []string{"sonnet-", "opus-"} {
		m = strings.TrimPrefix(m, family)
	}
	return modelVersionAtLeast(m, minMajor, minMinor)
}

// modelVersionAtLeast extracts a version from the start of s and checks >= major.minor.
// Supports "4.6", "4-5-20250514", "3-5-sonnet", "3-opus", "6" formats.
func modelVersionAtLeast(s string, minMajor, minMinor int) bool {
	var major, minor int
	if n, _ := fmt.Sscanf(s, "%d.%d", &major, &minor); n == 2 {
		return major > minMajor || (major == minMajor && minor >= minMinor)
	}
	if n, _ := fmt.Sscanf(s, "%d-%d", &major, &minor); n == 2 {
		return major > minMajor || (major == minMajor && minor >= minMinor)
	}
	if n, _ := fmt.Sscanf(s, "%d", &major); n == 1 {
		return major > minMajor
	}
	return false
}

// applyToolSearch splits tools into visible and deferred when the model
// supports deferred tool search. Non-core tools are deferred, reducing
// context bloat and improving tool selection accuracy.
// When the model is unsupported, both slices are returned unchanged.
func applyToolSearch(allTools, baseTools []agentcore.Tool, provider, model string) ([]agentcore.Tool, []agentcore.Tool) {
	if !supportsToolSearch(provider, model) {
		return allTools, baseTools
	}

	var visible, deferred []agentcore.Tool
	for _, t := range allTools {
		if coreToolNames[t.Name()] {
			visible = append(visible, t)
		} else {
			deferred = append(deferred, t)
		}
	}

	if len(deferred) == 0 {
		return allTools, baseTools
	}

	searchTool := agentcoretools.NewToolSearchTool(deferred...)

	// allTools = visible + searchTool + deferred (all registered, deferred hidden from LLM)
	result := make([]agentcore.Tool, 0, len(visible)+1+len(deferred))
	result = append(result, visible...)
	result = append(result, searchTool)
	result = append(result, deferred...)

	// Rebuild baseTools the same way (baseTools = allTools minus MCP tools).
	baseSet := make(map[string]bool, len(baseTools))
	for _, t := range baseTools {
		baseSet[t.Name()] = true
	}
	var newBase []agentcore.Tool
	for _, t := range result {
		if baseSet[t.Name()] || t.Name() == "tool_search" {
			newBase = append(newBase, t)
		}
	}

	return result, newBase
}

type systemParts struct {
	blocks      []agentcore.SystemBlock
	deferredMsg string   // <available-deferred-tools> content for first user message
	reminders   []string // <system-reminder> fragments for each user message
}

func buildSystemParts(cwd string, tools []agentcore.Tool, ctxFiles config.ContextFiles, skills []config.Skill, mcpManager *mcpclient.Manager) systemParts {
	// Separate visible tools from deferred tools.
	var filter agentcore.DeferFilter
	for _, t := range tools {
		if f, ok := t.(agentcore.DeferFilter); ok {
			filter = f
			break
		}
	}

	var visibleInfos []config.ToolInfo
	var deferredNames []string
	for _, t := range tools {
		if filter != nil && filter.IsDeferred(t.Name()) {
			deferredNames = append(deferredNames, t.Name())
		} else {
			visibleInfos = append(visibleInfos, config.ToolInfo{Name: t.Name(), Description: t.Description()})
		}
	}

	// Two-block system prompt: identity + instructions (both cached).
	identity, instructions := config.BuildSystemBlockTexts(cwd, ctxFiles, visibleInfos)

	// Append MCP instructions to the instructions block.
	if mcpManager != nil {
		if mcpInstructions := mcpManager.Instructions(); len(mcpInstructions) > 0 {
			for _, inst := range mcpInstructions {
				instructions += "\n\n" + inst
			}
		}
	}

	blocks := []agentcore.SystemBlock{
		{Text: identity, CacheControl: "ephemeral"},
		{Text: instructions, CacheControl: "ephemeral"},
	}

	// Git snapshot as a separate block — mirrors Claude Code's modular prompt assembly.
	if ctxFiles.GitSnapshot != "" {
		blocks = append(blocks, agentcore.SystemBlock{Text: ctxFiles.GitSnapshot})
	}

	// Deferred tools preamble for first user message.
	var deferredMsg string
	if len(deferredNames) > 0 {
		deferredMsg = "<available-deferred-tools>\n" + strings.Join(deferredNames, "\n") + "\n</available-deferred-tools>"
	}

	// Reminders: skills + context files for user message injection.
	reminders := config.BuildReminders(ctxFiles, skills)

	return systemParts{
		blocks:      blocks,
		deferredMsg: deferredMsg,
		reminders:   reminders,
	}
}

func buildRuntime(input *bootInput, spec *bootSpec) (*Runtime, error) {
	opts := []agentcore.AgentOption{
		agentcore.WithModel(spec.chatModel),
		agentcore.WithSystemBlocks(spec.systemBlocks),
		agentcore.WithTools(spec.tools...),
		agentcore.WithMaxTurns(spec.settings.MaxTurns),
		agentcore.WithMaxToolErrors(3),
		agentcore.WithMaxToolConcurrency(4),
		agentcore.WithContextPipeline(
			memory.NewCompaction(memory.CompactionConfig{
				Model:         spec.chatModel,
				ContextWindow: spec.settings.ContextWindow,
			}),
			memory.CompactionConvertToLLM,
		),
		agentcore.WithContextWindow(spec.settings.ContextWindow),
		agentcore.WithContextEstimate(memory.ContextEstimateAdapter),
		agentcore.WithToolApproval(spec.toolApproval),
	}
	if spec.hookMiddleware != nil {
		opts = append(opts, agentcore.WithMiddlewares(spec.hookMiddleware))
	}
	ag := agentcore.NewAgent(opts...)

	spec.subagentTool.SetNotifyFn(ag.FollowUp)

	// Wire output factories: consumer provides file I/O, agentcore just writes.
	sessionID := input.store.Header().SessionID
	tasksDir := filepath.Join(os.TempDir(), "codebot-bg", sessionID, "tasks")
	subagentsDir := filepath.Join(config.SessionsDir(input.cwd), sessionID, "subagents")
	spec.subagentTool.SetBgOutputFactory(func(taskID, agentName string) (io.WriteCloser, string, error) {
		_ = os.MkdirAll(subagentsDir, 0o700)
		path := filepath.Join(subagentsDir, "agent-"+taskID+".jsonl")
		f, err := os.Create(path)
		if err != nil {
			return nil, "", err
		}
		meta, _ := json.Marshal(map[string]string{"agentType": agentName})
		_ = os.WriteFile(filepath.Join(subagentsDir, "agent-"+taskID+".meta.json"), meta, 0o600)
		_ = os.MkdirAll(tasksDir, 0o700)
		_ = os.Symlink(path, filepath.Join(tasksDir, "agent-"+taskID+".output"))
		return f, path, nil
	})
	if spec.bashTool != nil {
		spec.bashTool.SetNotifyFn(ag.FollowUp)
		spec.bashTool.SetBgOutputFactory(func(shellID string) (io.WriteCloser, string, error) {
			_ = os.MkdirAll(tasksDir, 0o700)
			path := filepath.Join(tasksDir, shellID+".output")
			f, err := os.Create(path)
			return f, path, err
		})
	}

	if len(input.snapshot.Messages) > 0 {
		if err := ag.SetMessages(input.snapshot.Messages); err != nil {
			return nil, fmt.Errorf("restore agent messages: %w", err)
		}
		agentcore.ReactivateDeferred(spec.tools, input.snapshot.Messages)
	}
	if input.snapshot.Thinking != "" {
		ag.SetThinkingLevel(agentcore.ThinkingLevel(input.snapshot.Thinking))
		spec.settings.ThinkingLevel = input.snapshot.Thinking
	} else if spec.settings.ThinkingLevel != "" {
		ag.SetThinkingLevel(agentcore.ThinkingLevel(spec.settings.ThinkingLevel))
	}

	sess := agent.NewSession(agent.SessionConfig{
		Agent:                 ag,
		Store:                 input.store,
		Manager:               input.manager,
		Registry:              input.registry,
		Settings:              spec.settings,
		Cwd:                   input.cwd,
		CreateModel:           input.createModel,
		ChatModel:             spec.chatModel,
		Tools:                 spec.tools,
		ContextFiles:          spec.contextFiles,
		Skills:                spec.skills,
		HookRunner:            spec.hookRunner,
		DeferredToolsPreamble: spec.deferredToolsPreamble,
		Reminders:             spec.reminders,
		PreambleInjected:      len(input.snapshot.Messages) > 0, // resume: preamble already in history
	})

	if spec.mcpManager != nil {
		sess.SetBeforePrompt(func() {
			mcpTools, ok := spec.mcpManager.RefreshIfDirty(context.Background())
			if !ok {
				return
			}
			all := make([]agentcore.Tool, len(spec.baseTools), len(spec.baseTools)+len(mcpTools))
			copy(all, spec.baseTools)
			all = append(all, mcpTools...)
			spec.approvalEngine.ReplaceToolMetadata(all)
			sess.ReplaceAllTools(all)
		})
	}

	return &Runtime{
		Cwd:            input.cwd,
		GitBranch:      detectGitBranch(input.cwd),
		ApprovalEngine: spec.approvalEngine,
		Settings:       spec.settings,
		Session:        sess,
		MCPManager:     spec.mcpManager,
		MCPServers:     spec.mcpServers,
		EnvHint:        input.envHint,
	}, nil
}
