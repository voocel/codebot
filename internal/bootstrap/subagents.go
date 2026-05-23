package bootstrap

import (
	"strings"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/subagent"
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

// readOnlyDisallowed are the mutating tools excluded from read-only agents
// (explore, plan). They cover every way a sub-agent could touch disk or
// shell state. Listed here rather than at the call site so adding a new
// mutating tool elsewhere triggers a single review point.
var readOnlyDisallowed = []string{"write", "edit", "bash"}

// buildSubAgentTool constructs a SubAgentTool with all sub-agent types registered.
//
// Every sub-agent's tool pool flows through the same pipeline:
//
//	deps.AllTools  ──►  subagentToolPool (per-agent independent read/write/edit)
//	               ──►  agent.FilterToolsForAgent (three-layer rules)
//
// The pool step is invoked once per agent so each gets its own FileReadState
// — that prevents a read in explore from masking a missing read-before-write
// in coder, both of which run as different sub-agent kinds but share the
// same builtTools input. The filter step enforces global / async / per-agent
// denies and strips the `subagent` tool itself to prevent recursive spawn.
func buildSubAgentTool(deps subAgentDeps) *subagent.Tool {
	exploreTools := agent.FilterToolsForAgent(
		subagentToolPool(deps.Cwd, deps.AllTools),
		agent.FilterOpts{
			IsBuiltIn:       true,
			IsAsync:         true,
			AllowMCP:        true,
			ExtraDisallowed: readOnlyDisallowed,
		},
	)
	planTools := agent.FilterToolsForAgent(
		subagentToolPool(deps.Cwd, deps.AllTools),
		agent.FilterOpts{
			IsBuiltIn:       true,
			IsAsync:         true,
			AllowMCP:        true,
			ExtraDisallowed: readOnlyDisallowed,
		},
	)
	coderTools := agent.FilterToolsForAgent(
		subagentToolPool(deps.Cwd, deps.AllTools),
		agent.FilterOpts{
			IsBuiltIn: true,
			IsAsync:   false,
			AllowMCP:  true,
		},
	)

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

	sat := subagent.New(
		subagent.Config{
			Name:         "explore",
			Description:  "Fast codebase exploration agent. Use when you need to find files by patterns, search code for keywords, or answer questions about the codebase (e.g. 'how does authentication work?'). Read-only, no modifications.",
			Model:        exploreModel,
			SystemPrompt: config.ExploreSubAgentPrompt(deps.Cwd),
			Tools:        exploreTools,
			MaxTurns:     20,
			ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
				return newSubAgentContextManager(model, deps.ContextWindow)
			},
			ConvertToLLM: agentctx.ContextConvertToLLM,
		},
		subagent.Config{
			Name:         "plan",
			Description:  "Software architect. Explore code and design implementation strategies with step-by-step plans.",
			Model:        deps.Model,
			SystemPrompt: config.PlanSubAgentPrompt(deps.Cwd),
			Tools:        planTools,
			MaxTurns:     25,
			ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
				return newSubAgentContextManager(model, deps.ContextWindow)
			},
			ConvertToLLM: agentctx.ContextConvertToLLM,
		},
		subagent.Config{
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
				Classifier: agent.CodebotToolClassifier,
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

// subagentToolPool returns a tool list suitable as input to
// FilterToolsForAgent. Read / write / edit are replaced with fresh instances
// sharing a sub-agent-local FileReadState so the sub-agent cannot poison the
// parent's read-stamp cache (a read in a sub-agent must not let the parent
// silently skip the next read of the same file). Other tools are passed
// through by reference — they are either stateless or already keyed by cwd.
func subagentToolPool(cwd string, mainTools []agentcore.Tool) []agentcore.Tool {
	state := tools.NewFileReadState()
	out := make([]agentcore.Tool, 0, len(mainTools))
	for _, t := range mainTools {
		switch t.Name() {
		case "read":
			out = append(out, tools.NewRead(cwd, state))
		case "write":
			out = append(out, tools.NewWrite(cwd, state))
		case "edit":
			out = append(out, tools.NewEdit(cwd, state))
		default:
			out = append(out, t)
		}
	}
	return out
}
