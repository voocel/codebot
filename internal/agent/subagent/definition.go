package subagent

import (
	"fmt"
	"slices"

	"github.com/voocel/codebot/internal/agent/prompt"
)

const generalPurposeAgentName = "general-purpose"

type AgentDefinition struct {
	Name string
	// Description tells the model when to delegate to this agent.
	Description  string
	SystemPrompt string

	// Tools narrows the agent's tools; empty or {"*"} means all.
	Tools           []string
	DisallowedTools []string

	// Model is a model name; "inherit" or empty means the parent's.
	Model string

	// MaxTurns of zero means the subagent default.
	MaxTurns int

	// Origin is the definition's file or "builtin", for error messages.
	Origin string
}

func (d *AgentDefinition) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("agent definition missing name (from %s)", d.Origin)
	}
	if d.Description == "" {
		return fmt.Errorf("agent %q missing description (from %s)", d.Name, d.Origin)
	}
	if d.SystemPrompt == "" {
		return fmt.Errorf("agent %q missing system prompt body (from %s)", d.Name, d.Origin)
	}
	if slices.Contains(d.Tools, "") {
		return fmt.Errorf("agent %q has empty entry in tools list", d.Name)
	}
	if slices.Contains(d.DisallowedTools, "") {
		return fmt.Errorf("agent %q has empty entry in disallowedTools list", d.Name)
	}
	return nil
}

// mergeAgents lets later groups replace earlier definitions by name. A
// replacement is the whole definition, not a field merge: a user file that
// redeclares explore without disallowedTools drops its read-only restriction.
// Field merges are hard to predict, and a silent privilege change is worse
// than an explicit one.
func mergeAgents(groups ...[]AgentDefinition) []AgentDefinition {
	byName := make(map[string]AgentDefinition)
	var order []string
	for _, group := range groups {
		for _, def := range group {
			if _, seen := byName[def.Name]; !seen {
				order = append(order, def.Name)
			}
			byName[def.Name] = def
		}
	}
	out := make([]AgentDefinition, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out
}

// readOnlyDisallowed enforces what the explore and plan prompts only ask for.
var readOnlyDisallowed = []string{"write", "edit", "bash"}

func builtinDefinitions(cwd string) []AgentDefinition {
	return []AgentDefinition{
		{
			Name:            "explore",
			Description:     "Fast codebase exploration agent. Use when you need to find files by patterns, search code for keywords, or answer questions about the codebase (e.g. 'how does authentication work?'). Read-only, no modifications.",
			SystemPrompt:    prompt.ExploreAgent(cwd),
			DisallowedTools: readOnlyDisallowed,
			MaxTurns:        20,
			Origin:          "builtin",
		},
		{
			Name:            "plan",
			Description:     "Software architect. Explore code and design implementation strategies with step-by-step plans.",
			SystemPrompt:    prompt.PlanAgent(cwd),
			DisallowedTools: readOnlyDisallowed,
			MaxTurns:        25,
			Origin:          "builtin",
		},
		{
			Name:         generalPurposeAgentName,
			Description:  "General-purpose coding agent. Independently search, read, and write code to complete subtasks.",
			SystemPrompt: prompt.GeneralAgent(cwd),
			MaxTurns:     30,
			Origin:       "builtin",
		},
	}
}

// Definitions lets loaded definitions replace built-in ones by name.
// smallModel runs the built-in explore agent.
func Definitions(cwd, smallModel string, loaded []AgentDefinition) []AgentDefinition {
	builtin := builtinDefinitions(cwd)
	for i := range builtin {
		if builtin[i].Name == "explore" {
			builtin[i].Model = smallModel
		}
	}
	return mergeAgents(builtin, loaded)
}
