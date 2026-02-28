package bootstrap

import (
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
)

// subAgentDeps holds everything needed to build and configure the SubAgentTool.
type subAgentDeps struct {
	Cwd      string
	Model    agentcore.ChatModel // main agent's model (inherited by plan/coder)
	AllTools []agentcore.Tool    // main agent's tools BEFORE subagent is appended

	// For creating alternative models (e.g. a cheaper model for explore).
	CreateModel agent.ModelFactory
	Provider    string
	APIKey      string
	BaseURL     string
	SmallModel  string // model name for explore sub-agent, passed as-is to the provider
}

// buildSubAgentTool constructs a SubAgentTool with all sub-agent types registered.
func buildSubAgentTool(deps subAgentDeps) *agentcore.SubAgentTool {
	readOnly := readOnlyTools(deps.Cwd)
	coderTools := filterOutSubagent(deps.AllTools)

	exploreModel := deps.Model
	if deps.SmallModel != "" && deps.CreateModel != nil {
		if m, err := deps.CreateModel(deps.Provider, deps.SmallModel, deps.APIKey, deps.BaseURL); err == nil {
			exploreModel = m
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
		},
		agentcore.SubAgentConfig{
			Name:         "plan",
			Description:  "Software architect. Explore code and design implementation strategies with step-by-step plans.",
			Model:        deps.Model,
			SystemPrompt: config.PlanSubAgentPrompt(deps.Cwd),
			Tools:        readOnly,
			MaxTurns:     25,
		},
		agentcore.SubAgentConfig{
			Name:         "coder",
			Description:  "General-purpose coding agent. Independently search, read, and write code to complete subtasks.",
			Model:        deps.Model,
			SystemPrompt: config.CoderSubAgentPrompt(deps.Cwd),
			Tools:        coderTools,
			MaxTurns:     30,
		},
	)

	// Enable LLM to override model at call time via the "model" parameter.
	if deps.CreateModel != nil {
		factory := deps.CreateModel
		prov, apiKey, baseURL := deps.Provider, deps.APIKey, deps.BaseURL
		sat.SetCreateModel(func(name string) (agentcore.ChatModel, error) {
			return factory(prov, name, apiKey, baseURL)
		})
	}

	return sat
}

// readOnlyTools constructs a read-only tool set for explore/plan sub-agents.
func readOnlyTools(cwd string) []agentcore.Tool {
	return []agentcore.Tool{
		tools.NewRead(cwd),
		tools.NewFind(cwd),
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
