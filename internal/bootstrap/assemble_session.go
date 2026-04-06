package bootstrap

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/hooks"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/skill"
	localtools "github.com/voocel/codebot/internal/tools"
)

type sessionAssembly struct {
	settings              config.Resolved
	chatModel             agentcore.ChatModel
	tools                 []agentcore.Tool
	baseTools             []agentcore.Tool
	systemBlocks          []agentcore.SystemBlock
	deferredToolsPreamble string
	reminders             []string
	contextFiles          config.ContextFiles
	hookMiddleware        agentcore.ToolMiddleware
	hookRunner            *hooks.Runner
	subagentTool          *agentcore.SubAgentTool
	bashTool              *agentcoretools.BashTool
}

func buildSessionAssembly(input *resolvedInput, services *bootServices, factories []ToolFactory) (*sessionAssembly, error) {
	settings, activeProvider, chatModel, err := resolveActiveModel(input)
	if err != nil {
		return nil, err
	}

	ctxFiles := config.LoadContextFiles(input.cwd)
	ctxFiles.GitSnapshot = config.CollectGitSnapshot(input.cwd)
	ctxFiles.Memory, ctxFiles.MemoryDir = config.LoadMemory(input.cwd)
	config.EnsureMemoryDir(input.cwd)

	tools, baseTools, subagentTool, bashTool, err := buildToolset(input, services, settings, activeProvider, chatModel, factories)
	if err != nil {
		return nil, err
	}

	wireSkillAllows(tools, services.approvalEngine)

	parts := buildSystemParts(input.cwd, tools, ctxFiles, services.skills, skillUsageScores(services.skillUsage), services.mcpManager)
	hookMiddleware, hookRunner := buildHookSupport(input, services, settings, chatModel)

	return &sessionAssembly{
		settings:              settings,
		chatModel:             chatModel,
		tools:                 tools,
		baseTools:             baseTools,
		systemBlocks:          parts.blocks,
		deferredToolsPreamble: parts.deferredMsg,
		reminders:             parts.reminders,
		contextFiles:          ctxFiles,
		hookMiddleware:        hookMiddleware,
		hookRunner:            hookRunner,
		subagentTool:          subagentTool,
		bashTool:              bashTool,
	}, nil
}

func resolveActiveModel(input *resolvedInput) (config.Resolved, string, agentcore.ChatModel, error) {
	activeProvider := input.settings.Provider
	if input.sessionSnapshot.Provider != "" {
		activeProvider = input.sessionSnapshot.Provider
	}
	activeModel := input.settings.Model
	if input.sessionSnapshot.Model != "" {
		activeModel = input.sessionSnapshot.Model
	}

	activeAPIKey, activeBaseURL := input.settings.ProviderCredentials(activeProvider)
	provType := "openai"
	if pc, ok := input.settings.Providers[activeProvider]; ok {
		provType = pc.ProviderType(activeProvider)
	} else if t, ok := config.KnownProviderTypes[activeProvider]; ok {
		provType = t
	}
	chatModel, err := input.modelFactory(provType, activeModel, activeAPIKey, activeBaseURL)
	if err != nil {
		return config.Resolved{}, "", nil, fmt.Errorf("create model: %w", err)
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

	return settings, activeProvider, chatModel, nil
}

func buildToolset(input *resolvedInput, services *bootServices, settings config.Resolved, activeProvider string, chatModel agentcore.ChatModel, factories []ToolFactory) ([]agentcore.Tool, []agentcore.Tool, *agentcore.SubAgentTool, *agentcoretools.BashTool, error) {
	builtTools := buildTools(input.cwd, factories)

	var bashTool *agentcoretools.BashTool
	for _, tool := range builtTools {
		if bt, ok := tool.(*agentcoretools.BashTool); ok {
			bashTool = bt
			break
		}
	}

	askTool := localtools.NewAskUser()
	_, cronTools := localtools.NewCronTools()
	builtTools = append(builtTools,
		localtools.NewWebFetch(settings.SearchProvider, settings.SearchAPIKey),
		localtools.NewWebSearch(settings.SearchProvider, settings.SearchAPIKey),
		askTool,
		localtools.NewEnterPlanMode(),
		localtools.NewExitPlanMode(),
	)
	builtTools = append(builtTools, cronTools...)

	toolOutputDir := filepath.Join(config.SessionsDir(input.cwd), input.sessionStore.Header().SessionID, "tool-outputs")
	localtools.SetOutputDir(toolOutputDir)
	builtTools = localtools.WrapWithOutputLimit(builtTools)

	subagentTool := buildSubAgentTool(subAgentDeps{
		Cwd:           input.cwd,
		Model:         chatModel,
		AllTools:      builtTools,
		ContextWindow: settings.ContextWindow,
		CreateModel:   input.modelFactory,
		Provider:      activeProvider,
		Providers:     settings.Providers,
		SmallModel:    settings.SmallModel,
	})
	builtTools = append(builtTools, subagentTool)

	skillTool := localtools.NewSkillTool(services.skillCatalog, input.sessionStore.Header().SessionID)
	skillTool.SetForkExecutor(subagentTool.Execute)
	builtTools = append(builtTools, skillTool)
	baseTools := builtTools

	builtTools, baseTools = applyToolSearch(builtTools, baseTools, activeProvider, settings.Model)
	return builtTools, baseTools, subagentTool, bashTool, nil
}

func wireSkillAllows(tools []agentcore.Tool, approvalEngine *approval.Engine) {
	for _, tool := range tools {
		if st, ok := tool.(*localtools.SkillTool); ok {
			st.SetAllowToolsSetter(approvalEngine.SetSkillAllows)
			return
		}
	}
}

func buildHookSupport(input *resolvedInput, services *bootServices, settings config.Resolved, chatModel agentcore.ChatModel) (agentcore.ToolMiddleware, *hooks.Runner) {
	if len(settings.Hooks) == 0 {
		return nil, nil
	}
	hookRunner := hooks.New(settings.Hooks, input.sessionStore.Header().SessionID, services.approvalEngine, chatModel)
	if hookRunner == nil {
		return nil, nil
	}
	return hookRunner.Middleware(), hookRunner
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

	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}

	if p == "anthropic" || strings.HasPrefix(m, "claude") {
		if strings.Contains(m, "haiku") {
			return false
		}
		return claudeVersionAtLeast(m, 4, 5)
	}

	if p == "openai" || strings.HasPrefix(m, "gpt-") {
		return modelVersionAtLeast(strings.TrimPrefix(m, "gpt-"), 5, 4)
	}

	return false
}

func claudeVersionAtLeast(model string, minMajor, minMinor int) bool {
	m := strings.TrimPrefix(model, "claude-")
	for _, family := range []string{"sonnet-", "opus-"} {
		m = strings.TrimPrefix(m, family)
	}
	return modelVersionAtLeast(m, minMajor, minMinor)
}

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
	result := make([]agentcore.Tool, 0, len(visible)+1+len(deferred))
	result = append(result, visible...)
	result = append(result, searchTool)
	result = append(result, deferred...)

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
	deferredMsg string
	reminders   []string
}

func buildSystemParts(cwd string, tools []agentcore.Tool, ctxFiles config.ContextFiles, skills []skill.Spec, usage map[string]float64, mcpManager *mcpclient.Manager) systemParts {
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

	identity, instructions := config.BuildSystemBlockTexts(cwd, ctxFiles, visibleInfos)
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
	if ctxFiles.GitSnapshot != "" {
		blocks = append(blocks, agentcore.SystemBlock{Text: ctxFiles.GitSnapshot})
	}

	var deferredMsg string
	if len(deferredNames) > 0 {
		deferredMsg = "<available-deferred-tools>\n" + strings.Join(deferredNames, "\n") + "\n</available-deferred-tools>"
	}

	return systemParts{
		blocks:      blocks,
		deferredMsg: deferredMsg,
		reminders:   config.BuildReminders(ctxFiles, skill.OrderForPrompt(skills, cwd, usage)),
	}
}
