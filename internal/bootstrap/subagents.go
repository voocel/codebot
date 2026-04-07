package bootstrap

import (
	"strings"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
)

// subAgentDeps holds everything needed to build and configure the SubAgentTool.
type subAgentDeps struct {
	Cwd           string
	Model         agentcore.ChatModel // main agent's model (inherited by plan/coder)
	AllTools      []agentcore.Tool    // main agent's tools BEFORE subagent is appended
	ContextWindow int

	// For creating alternative models (e.g. a cheaper model for explore).
	CreateModel agent.ModelFactory
	Provider    string                           // main provider name
	Providers   map[string]config.ProviderConfig // per-provider credentials
	SmallModel  string                           // model name for explore sub-agent
}

// buildSubAgentTool constructs a SubAgentTool with all sub-agent types registered.
func buildSubAgentTool(deps subAgentDeps) *agentcore.SubAgentTool {
	readOnly := readOnlyTools(deps.Cwd)
	coderTools := filterOutSubagent(deps.AllTools)

	exploreModel := deps.Model
	if deps.SmallModel != "" && deps.CreateModel != nil {
		prov := deps.Provider
		apiKey, baseURL := resolveFromProviders(deps.Providers, prov)
		provType, err := resolveProviderType(deps.Providers, prov)
		if err == nil {
			if m, err := deps.CreateModel(provType, deps.SmallModel, apiKey, baseURL); err == nil {
				exploreModel = m
			}
		}
	}

	sat := agentcore.NewSubAgentTool(
		agentcore.SubAgentConfig{
			Name:         "explore",
			Description:  "Fast codebase exploration agent. Use when you need to find files by patterns, search code for keywords, or answer questions about the codebase (e.g. 'how does authentication work?'). Read-only, no modifications.",
			Model:        exploreModel,
			SystemPrompt: config.ExploreSubAgentPrompt(deps.Cwd),
			Tools:        readOnly,
			MaxTurns:     20,
			ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
				return newSubAgentContextManager(model, deps.ContextWindow)
			},
			ConvertToLLM: agentctx.ContextConvertToLLM,
		},
		agentcore.SubAgentConfig{
			Name:         "plan",
			Description:  "Software architect. Explore code and design implementation strategies with step-by-step plans.",
			Model:        deps.Model,
			SystemPrompt: config.PlanSubAgentPrompt(deps.Cwd),
			Tools:        readOnly,
			MaxTurns:     25,
			ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
				return newSubAgentContextManager(model, deps.ContextWindow)
			},
			ConvertToLLM: agentctx.ContextConvertToLLM,
		},
		// NOTE: coder subagent intentionally does NOT have task_* tools.
		// The main agent owns the task list; short-lived subagents report
		// results back and the main agent updates task status.
		// TODO(team): when teammate (long-running agent) is implemented,
		// grant task_* tools so teammates can coordinate via shared tasks.
		agentcore.SubAgentConfig{
			Name:         "coder",
			Description:  "General-purpose coding agent. Independently search, read, and write code to complete subtasks.",
			Model:        deps.Model,
			SystemPrompt: config.CoderSubAgentPrompt(deps.Cwd),
			Tools:        coderTools,
			MaxTurns:     30,
			ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
				return newSubAgentContextManager(model, deps.ContextWindow)
			},
			ConvertToLLM: agentctx.ContextConvertToLLM,
		},
	)

	// Enable LLM to override model at call time via the "model" parameter.
	if deps.CreateModel != nil {
		factory := deps.CreateModel
		providers := deps.Providers
		defaultProv := deps.Provider
		sat.SetCreateModel(func(name string) (agentcore.ChatModel, error) {
			// Search all providers' model lists for an exact match.
			prov := defaultProv
			for provName, pc := range providers {
				for _, m := range pc.Models {
					if strings.EqualFold(m, name) {
						prov = provName
						break
					}
				}
			}
			apiKey, baseURL := resolveFromProviders(providers, prov)
			provType, err := resolveProviderType(providers, prov)
			if err != nil {
				return nil, err
			}
			return factory(provType, name, apiKey, baseURL)
		})
	}

	return sat
}

func newSubAgentContextManager(model agentcore.ChatModel, window int) agentcore.ContextManager {
	if model == nil || window <= 0 {
		return nil
	}
	return agentctx.NewEngine(agentctx.EngineConfig{
		ContextWindow: window,
		Strategies: []agentctx.Strategy{
			agentctx.NewToolResultMicrocompact(agentctx.ToolResultMicrocompactConfig{
				KeepRecent: 3,
			}),
			agentctx.NewLightTrim(agentctx.LightTrimConfig{}),
			agentctx.NewFullSummary(agentctx.FullSummaryConfig{
				Model:            model,
				KeepRecentTokens: 12000,
			}),
		},
	})
}

// resolveFromProviders returns credentials for a provider from the map,
// falling back to standard environment variables.
func resolveFromProviders(providers map[string]config.ProviderConfig, prov string) (apiKey, baseURL string) {
	if pc, ok := providers[prov]; ok && pc.APIKey != "" {
		return pc.APIKey, pc.BaseURL
	}
	return config.EnvCredentials(prov)
}

// resolveProviderType returns the protocol type for a provider key.
func resolveProviderType(providers map[string]config.ProviderConfig, prov string) (string, error) {
	return config.ResolveConfiguredProviderType(providers, prov)
}

// readOnlyTools constructs a read-only tool set for explore/plan sub-agents.
func readOnlyTools(cwd string) []agentcore.Tool {
	return []agentcore.Tool{
		tools.NewRead(cwd),
		tools.NewGlob(cwd),
		tools.NewGrep(cwd),
		tools.NewLs(cwd),
	}
}

// filterOutSubagent removes the subagent tool from a tool list to prevent nesting.
func filterOutSubagent(all []agentcore.Tool) []agentcore.Tool {
	filtered := make([]agentcore.Tool, 0, len(all))
	for _, t := range all {
		if t.Name() != "subagent" {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
