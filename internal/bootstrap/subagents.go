package bootstrap

import (
	"fmt"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/provider"
)

// subAgentDeps holds everything needed to build and configure the SubAgentTool.
type subAgentDeps struct {
	Cwd      string
	Model    agentcore.ChatModel // main agent's model (inherited by plan/coder)
	AllTools []agentcore.Tool    // main agent's tools BEFORE subagent is appended

	// For creating alternative models (e.g. a cheaper model for explore).
	CreateModel  agent.ModelFactory
	Registry     *provider.ModelRegistry
	Provider     string
	APIKey       string
	BaseURL      string
	ExploreModel string // optional model pattern for explore (e.g. "haiku", "gpt-4o-mini")
}

// buildSubAgentTool constructs a SubAgentTool with all sub-agent types registered.
func buildSubAgentTool(deps subAgentDeps) *agentcore.SubAgentTool {
	readOnly := readOnlyTools(deps.Cwd)
	coderTools := filterOutSubagent(deps.AllTools)

	exploreModel := deps.Model
	if deps.ExploreModel != "" {
		if m, err := resolveModel(deps); err == nil {
			exploreModel = m
		}
	}

	sat := agentcore.NewSubAgentTool(
		agentcore.SubAgentConfig{
			Name:         "explore",
			Description:  "Fast code exploration. Search files, match patterns, read code. Read-only.",
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
	if deps.CreateModel != nil && deps.Registry != nil {
		sat.SetCreateModel(func(name string) (agentcore.ChatModel, error) {
			entry, _, err := deps.Registry.Resolve(name)
			if err != nil {
				return nil, err
			}
			apiKey, baseURL := deps.APIKey, deps.BaseURL
			if entry.Provider != deps.Provider {
				apiKey, baseURL = "", "" // let factory resolve from env
			}
			return deps.CreateModel(entry.Provider, entry.ID, apiKey, baseURL)
		})
	}

	return sat
}

// resolveModel resolves deps.ExploreModel to a ChatModel instance.
func resolveModel(deps subAgentDeps) (agentcore.ChatModel, error) {
	if deps.Registry == nil || deps.CreateModel == nil {
		return nil, fmt.Errorf("model registry or factory not available")
	}
	entry, _, err := deps.Registry.Resolve(deps.ExploreModel)
	if err != nil {
		return nil, err
	}
	apiKey, baseURL := deps.APIKey, deps.BaseURL
	if entry.Provider != deps.Provider {
		apiKey, baseURL = "", ""
	}
	return deps.CreateModel(entry.Provider, entry.ID, apiKey, baseURL)
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
