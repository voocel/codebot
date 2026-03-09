package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/memory"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/hooks"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/storage"
	localtools "github.com/voocel/codebot/internal/tools"
)

type bootInput struct {
	cwd         string
	settings    config.Resolved
	registry    *provider.ModelRegistry
	profile     policy.Profile
	createModel agent.ModelFactory
	manager     *storage.Manager
	store       *storage.Store
	snapshot    storage.ContextSnapshot
	nonTTY      bool
}

type bootSpec struct {
	settings       config.Resolved
	activeProvider string
	activeModel    string
	chatModel      agentcore.ChatModel
	tools          []agentcore.Tool
	baseTools      []agentcore.Tool
	systemPrompt   string
	contextFiles   config.ContextFiles
	skills         []config.Skill
	mcpManager     *mcpclient.Manager
	subagentTool   *agentcore.SubAgentTool
	permission     func(context.Context, agentcore.ToolCall) error
	policyEngine   *policy.Engine
	hookMiddleware agentcore.ToolMiddleware // nil = no hooks configured
	hookRunner     *hooks.Runner
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

	profile, err := parseProfile(opts.PolicyProfile)
	if err != nil {
		return nil, err
	}

	createModel := opts.ModelFactory
	if createModel == nil {
		createModel = provider.CreateModel
	}

	settings, err = ensureProviderSetup(cwd, settings, registry, opts.NonTTYMode)
	if err != nil {
		return nil, err
	}

	manager := storage.NewManager(config.SessionsDir(cwd))
	store, err := resolveSession(manager, cwd, opts.Continue, opts.Resume, opts.Session, opts.NonTTYMode)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}

	var snapshot storage.ContextSnapshot
	if opts.Continue || opts.Resume || opts.Session != "" {
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
	}, nil
}

func ensureProviderSetup(cwd string, settings config.Resolved, registry *provider.ModelRegistry, nonTTY bool) (config.Resolved, error) {
	apiKey, _ := settings.ProviderCredentials(settings.Provider)
	if apiKey != "" {
		return settings, nil
	}
	if nonTTY {
		return settings, fmt.Errorf("api key not set; set %s or configure providers in %s",
			config.ProviderEnvKey(settings.Provider), filepath.Join(config.UserConfigDir(), "settings.json"))
	}

	err := config.RunSetup(settings, func(prov string) []config.ModelOption {
		entries := registry.FindByProvider(prov)
		result := make([]config.ModelOption, len(entries))
		for i, e := range entries {
			result[i] = config.ModelOption{ID: e.ID, Name: e.Name}
		}
		return result
	})
	if err != nil {
		return settings, fmt.Errorf("setup: %w", err)
	}
	return config.ResolveAll(cwd), nil
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
	skills := config.LoadSkills(input.cwd)

	pol := policy.New(policy.Config{
		Profile:         input.profile,
		Workspace:       input.cwd,
		Interactive:     !input.nonTTY,
		OnAudit:         fileAuditor(config.AuditLogPath()),
		AllowedCommands: settings.AllowedCommands,
	})
	cwd := input.cwd
	pol.SetPersistFn(func(cmd string) error {
		return config.AddAllowedCommand(cwd, cmd)
	})

	tools, baseTools, mcpManager, subagentTool, err := buildToolset(input, settings, activeProvider, chatModel, factories)
	if err != nil {
		return nil, err
	}

	systemPrompt := buildSystemPrompt(input.cwd, tools, ctxFiles, skills, mcpManager)

	var hookMW agentcore.ToolMiddleware
	var hookRunner *hooks.Runner
	if len(settings.Hooks) > 0 {
		hookRunner = hooks.New(settings.Hooks, input.store.Header().SessionID)
		if hookRunner != nil {
			hookMW = hookRunner.Middleware()
		}
	}

	return &bootSpec{
		settings:       settings,
		activeProvider: activeProvider,
		activeModel:    activeModel,
		chatModel:      chatModel,
		tools:          tools,
		baseTools:      baseTools,
		systemPrompt:   systemPrompt,
		contextFiles:   ctxFiles,
		skills:         skills,
		mcpManager:     mcpManager,
		subagentTool:   subagentTool,
		permission:     pol.Permission,
		policyEngine:   pol,
		hookMiddleware: hookMW,
		hookRunner:     hookRunner,
	}, nil
}

func buildToolset(input *bootInput, settings config.Resolved, activeProvider string, chatModel agentcore.ChatModel, factories []ToolFactory) ([]agentcore.Tool, []agentcore.Tool, *mcpclient.Manager, *agentcore.SubAgentTool, error) {
	builtTools := buildTools(input.cwd, factories)
	askTool := localtools.NewAskUser()
	taskStore, taskTools := localtools.NewTaskTools()
	builtTools = append(builtTools,
		localtools.NewWebFetch(settings.SearchProvider, settings.SearchAPIKey),
		localtools.NewWebSearch(settings.SearchProvider, settings.SearchAPIKey),
		askTool,
	)
	builtTools = append(builtTools, taskTools...)
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
		if errs := mcpManager.StartAll(context.Background(), mcpServers); len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "mcp: %v\n", e)
			}
		}
		builtTools = append(builtTools, mcpManager.Tools(context.Background())...)
	}

	return builtTools, baseTools, mcpManager, subagentTool, nil
}

func buildSystemPrompt(cwd string, tools []agentcore.Tool, ctxFiles config.ContextFiles, skills []config.Skill, mcpManager *mcpclient.Manager) string {
	toolInfos := make([]config.ToolInfo, len(tools))
	for i, t := range tools {
		toolInfos[i] = config.ToolInfo{Name: t.Name(), Description: t.Description()}
	}

	systemPrompt := config.BuildSystemPrompt(cwd, ctxFiles, toolInfos, skills)
	if mcpManager == nil {
		return systemPrompt
	}
	if instructions := mcpManager.Instructions(); len(instructions) > 0 {
		var sb strings.Builder
		sb.WriteString(systemPrompt)
		for _, inst := range instructions {
			sb.WriteString("\n\n")
			sb.WriteString(inst)
		}
		systemPrompt = sb.String()
	}
	return systemPrompt
}

func buildRuntime(input *bootInput, spec *bootSpec) (*Runtime, error) {
	opts := []agentcore.AgentOption{
		agentcore.WithModel(spec.chatModel),
		agentcore.WithSystemPrompt(spec.systemPrompt),
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
		agentcore.WithPermission(spec.permission),
	}
	if spec.hookMiddleware != nil {
		opts = append(opts, agentcore.WithMiddlewares(spec.hookMiddleware))
	}
	ag := agentcore.NewAgent(opts...)

	spec.subagentTool.SetNotifyFn(ag.FollowUp)

	if len(input.snapshot.Messages) > 0 {
		if err := ag.SetMessages(input.snapshot.Messages); err != nil {
			return nil, fmt.Errorf("restore agent messages: %w", err)
		}
	}
	if input.snapshot.Thinking != "" {
		ag.SetThinkingLevel(agentcore.ThinkingLevel(input.snapshot.Thinking))
		spec.settings.ThinkingLevel = input.snapshot.Thinking
	} else if spec.settings.ThinkingLevel != "" {
		ag.SetThinkingLevel(agentcore.ThinkingLevel(spec.settings.ThinkingLevel))
	}

	sess := agent.NewSession(agent.SessionConfig{
		Agent:        ag,
		Store:        input.store,
		Manager:      input.manager,
		Registry:     input.registry,
		Settings:     spec.settings,
		Cwd:          input.cwd,
		CreateModel:  input.createModel,
		ChatModel:    spec.chatModel,
		Tools:        spec.tools,
		ContextFiles: spec.contextFiles,
		Skills:       spec.skills,
		HookRunner:   spec.hookRunner,
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
			sess.ReplaceAllTools(all)
		})
	}

	return &Runtime{
		Cwd:           input.cwd,
		GitBranch:     detectGitBranch(input.cwd),
		PolicyProfile: input.profile,
		PolicyEngine:  spec.policyEngine,
		Settings:      spec.settings,
		Session:       sess,
		MCPManager:    spec.mcpManager,
	}, nil
}
