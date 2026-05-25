package bootstrap

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/diag"
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
	frozenIdentity        string // process-stable: block 1
	frozenInstructions    string // process-stable: block 2
	initialMCPOverlay     string // seed for session.overlays["mcp"]
	initialDynamic        string // seed for session.dynamicText (teammate spawn snapshot)
	deferredToolsPreamble string
	reminders             []string
	contextFiles          config.ContextFiles
	hookMiddleware        agentcore.ToolMiddleware
	hookRunner            *hooks.Runner
	subagentTool          *subagent.Tool
	bashTool              *agentcoretools.BashTool
	fileReadState         *agentcoretools.FileReadState
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

	// Per-session file read state. Shared by Read (writes stamps) and
	// Write/Edit (Validators read stamps to enforce read-before-write).
	fileReadState := agentcoretools.NewFileReadState()
	if factories == nil {
		factories = defaultToolFactories(fileReadState)
	}

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
		frozenIdentity:        parts.frozenIdentity,
		frozenInstructions:    parts.frozenInstructions,
		initialMCPOverlay:     parts.initialMCPOverlay,
		initialDynamic:        parts.initialDynamic,
		deferredToolsPreamble: parts.deferredMsg,
		reminders:             parts.reminders,
		contextFiles:          ctxFiles,
		hookMiddleware:        hookMiddleware,
		hookRunner:            hookRunner,
		subagentTool:          subagentTool,
		bashTool:              bashTool,
		fileReadState:         fileReadState,
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
	provType, err := config.ResolveConfiguredProviderType(input.settings.Providers, activeProvider)
	if err != nil {
		return config.Resolved{}, "", nil, err
	}
	chatModel, err := input.modelFactory(provType, activeModel, activeAPIKey, activeBaseURL)
	if err != nil {
		return config.Resolved{}, "", nil, fmt.Errorf("create model failed: %w: %w", diag.ErrProvider, err)
	}

	settings := input.settings
	if entry, _, err := input.registry.Resolve(activeModel); err == nil && entry.ContextWindow > 0 {
		settings.ContextWindow = entry.ContextWindow
	} else if settings.ContextWindow <= 0 {
		settings.ContextWindow = 128000
	}
	// Apply user cap: effective = min(detected, CompactWindow). Never raise above
	// the model's real window — that would trigger API errors.
	if cap := settings.CompactWindow; cap > 0 && cap < settings.ContextWindow {
		settings.ContextWindow = cap
	}
	settings.Provider = activeProvider
	settings.Model = activeModel

	return settings, activeProvider, chatModel, nil
}

func buildToolset(input *resolvedInput, services *bootServices, settings config.Resolved, activeProvider string, chatModel agentcore.ChatModel, factories []ToolFactory) ([]agentcore.Tool, []agentcore.Tool, *subagent.Tool, *agentcoretools.BashTool, error) {
	builtTools := buildTools(input.cwd, factories)

	var bashTool *agentcoretools.BashTool
	for _, tool := range builtTools {
		if bt, ok := tool.(*agentcoretools.BashTool); ok {
			bashTool = bt
			break
		}
	}

	_, cronTools := localtools.NewCronTools()
	builtTools = append(builtTools,
		localtools.NewWebFetch(settings.SearchProvider, settings.SearchAPIKey),
		localtools.NewWebSearch(settings.SearchProvider, settings.SearchAPIKey),
		localtools.NewEnterPlanMode(),
		// Keep exit_plan_mode in the base toolset: agentcore captures the tool
		// list at run start, so adding it dynamically after enter_plan_mode is
		// too late for the same planning run. Plan.Manager still filters the
		// visible active toolset and validates out-of-phase calls.
		localtools.NewExitPlanMode(),
	)
	// ask_user requires an interactive UI to relay questions to the user.
	// In non-TTY mode there is no one watching, so we hide the tool entirely
	// rather than exposing a stub that the model would still try to call.
	if !input.nonTTY {
		builtTools = append(builtTools, localtools.NewAskUser())
	}
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
// Tools NOT in this set are deferred behind tool_search when the model
// supports it. The default is opt-in: frequently used core tools stay
// in the main prompt so the model can call them turn 1 without a
// tool_search round-trip; rarely used or schema-heavy tools defer to
// keep the base prompt compact.
var coreToolNames = map[string]bool{
	// Filesystem + shell — used in virtually every turn.
	"read":  true,
	"write": true,
	"edit":  true,
	"bash":  true,
	"grep":  true,
	"glob":  true,
	"ls":    true,
	// Task management — if present, should be immediately callable
	// (the system prompt tells the model to use them proactively).
	"task_create": true,
	"task_update": true,
	"task_list":   true,
	"task_get":    true,
	// Interaction / plan mode — turn-1 UX primitives.
	"ask_user":        true,
	"enter_plan_mode": true,
	"exit_plan_mode":  true,
}

// supportsToolSearch reports whether the given provider/model combination
// supports deferred tool search.
//
// Only Anthropic-family models are enabled today. OpenAI-family paths are
// intentionally excluded — see the note below.
//
// OpenAI caveat (2026-04):
//
//	The deferred-tool-search protocol relies on two mechanisms that today are
//	Anthropic-specific in our stack:
//	  1. defer_loading: true on the tool spec      (litellm/anthropic.go:223)
//	  2. tool_reference content blocks expanded    (litellm/anthropic.go:825)
//	     server-side into the real tool schema
//
//	The OpenAI / OpenAI-compat providers in litellm do NOT honor either. On
//	those paths, "activating" a deferred tool simply re-adds its schema to
//	the tools[] array mid-session, which invalidates OpenAI's 24h prefix
//	cache every time the set changes — costing far more tokens than deferring
//	ever saved. So we opt OpenAI out and send every tool up-front, keeping
//	the prefix identical across turns.
//
//	GPT-5.4 reportedly has native tool-search support on the official
//	endpoint, but we have not wired litellm's openai provider to emit the
//	right request shape yet. Revisit here when that support lands —
//	the block below is the single switch to flip.
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

	// OpenAI-family: intentionally disabled. See header comment for rationale.
	// When litellm's openai provider grows native tool_reference / deferred
	// loading support, re-enable here:
	//   if p == "openai" || strings.HasPrefix(m, "gpt-") {
	//       return modelVersionAtLeast(strings.TrimPrefix(m, "gpt-"), 5, 4)
	//   }

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
	blocks             []agentcore.SystemBlock
	frozenIdentity     string
	frozenInstructions string
	initialMCPOverlay  string
	initialDynamic     string
	deferredMsg        string
	reminders          []string
}

// buildSystemParts assembles the initial system blocks plus the inputs
// Session needs to keep rebuilding only the dynamic tail later:
//
//   - frozenIdentity / frozenInstructions are baked from local tools and
//     ctxFiles and never recomputed during the process. Session reuses them
//     verbatim on every rebuild.
//   - initialMCPOverlay seeds session.overlays["mcp"] so the first rebuild
//     after session creation reproduces the same dynamic block bytes.
//   - blocks is the system layout for the initial agent request: block 1
//     (identity) + block 2 (instructions) carry CacheControl: ephemeral;
//     block 3 (dynamic) carries no cache_control because it changes when
//     MCP refreshes or plan-mode toggles.
func buildSystemParts(cwd string, tools []agentcore.Tool, ctxFiles config.ContextFiles, skills []skill.Spec, usage map[string]float64, mcpManager *mcpclient.Manager) systemParts {
	var filter agentcore.DeferFilter
	for _, t := range tools {
		if f, ok := t.(agentcore.DeferFilter); ok {
			filter = f
			break
		}
	}

	var localInfos, mcpInfos []config.ToolInfo
	var deferredNames []string
	for _, t := range tools {
		name := t.Name()
		if filter != nil && filter.IsDeferred(name) {
			deferredNames = append(deferredNames, name)
			continue
		}
		info := config.ToolInfo{Name: name, Description: t.Description()}
		if config.IsMCPTool(name) {
			mcpInfos = append(mcpInfos, info)
		} else {
			localInfos = append(localInfos, info)
		}
	}

	identity, frozenInstructions := config.BuildFrozenSystemParts(cwd, ctxFiles, localInfos)

	var mcpOverlay string
	var overlayTexts []string
	if mcpManager != nil {
		if inst := mcpManager.Instructions(); len(inst) > 0 {
			mcpOverlay = strings.Join(inst, "\n\n")
			overlayTexts = append(overlayTexts, mcpOverlay)
		}
	}
	dynamic := config.BuildDynamicSystemPart(mcpInfos, overlayTexts)

	blocks := []agentcore.SystemBlock{
		{Text: identity, CacheControl: "ephemeral"},
		{Text: frozenInstructions, CacheControl: "ephemeral"},
	}
	if dynamic != "" {
		blocks = append(blocks, agentcore.SystemBlock{Text: dynamic})
	}
	if ctxFiles.GitSnapshot != "" {
		blocks = append(blocks, agentcore.SystemBlock{Text: ctxFiles.GitSnapshot})
	}

	var deferredMsg string
	if len(deferredNames) > 0 {
		deferredMsg = "<available-deferred-tools>\n" + strings.Join(deferredNames, "\n") + "\n</available-deferred-tools>"
	}

	return systemParts{
		blocks:             blocks,
		frozenIdentity:     identity,
		frozenInstructions: frozenInstructions,
		initialMCPOverlay:  mcpOverlay,
		initialDynamic:     dynamic,
		deferredMsg:        deferredMsg,
		reminders:          config.BuildReminders(ctxFiles, skill.OrderForPrompt(skills, cwd, usage)),
	}
}
