package bootstrap

import (
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/config"
)

// buildSubAgentTool constructs a SubAgentTool with all sub-agent types registered.
//
// Parameters:
//   - cwd: working directory for tool instances
//   - model: the main agent's ChatModel (sub-agents inherit it)
//   - allTools: the main agent's tool set BEFORE subagent is appended
//     (coder sub-agent uses this set minus the subagent tool to prevent nesting)
func buildSubAgentTool(cwd string, model agentcore.ChatModel, allTools []agentcore.Tool) *agentcore.SubAgentTool {
	readOnly := readOnlyTools(cwd)
	coderTools := filterOutSubagent(allTools)

	return agentcore.NewSubAgentTool(
		agentcore.SubAgentConfig{
			Name:         "explore",
			Description:  "Fast code exploration. Search files, match patterns, read code. Read-only.",
			Model:        model,
			SystemPrompt: config.ExploreSubAgentPrompt(cwd),
			Tools:        readOnly,
			MaxTurns:     20,
		},
		agentcore.SubAgentConfig{
			Name:         "plan",
			Description:  "Software architect. Explore code and design implementation strategies with step-by-step plans.",
			Model:        model,
			SystemPrompt: config.PlanSubAgentPrompt(cwd),
			Tools:        readOnly,
			MaxTurns:     25,
		},
		agentcore.SubAgentConfig{
			Name:         "coder",
			Description:  "General-purpose coding agent. Independently search, read, and write code to complete subtasks.",
			Model:        model,
			SystemPrompt: config.CoderSubAgentPrompt(cwd),
			Tools:        coderTools,
			MaxTurns:     30,
		},
	)
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
