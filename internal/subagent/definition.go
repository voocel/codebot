package subagent

import (
	"fmt"
	"log"
	"path/filepath"
	"slices"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/prompt"
)

const generalPurposeAgentName = "general-purpose"

// AgentDefinition describes a sub-agent: a built-in one, or one loaded from
// a .codebot/agents/*.md file in the project or the user's home.
type AgentDefinition struct {
	// Name is the identifier the model passes to the subagent tool.
	Name string

	// Description tells the model when to delegate to this agent.
	Description string

	SystemPrompt string

	// Tools narrows the agent to these tools; empty or {"*"} keeps all it
	// may have.
	Tools []string

	// DisallowedTools are tools the agent does not get.
	DisallowedTools []string

	// Model is "inherit" or empty for the parent's model, or a model name.
	Model string

	// MaxTurns caps the agent's loop. Zero means use the subagent default.
	MaxTurns int

	// Origin is where the definition came from, for error messages: its
	// file, or "builtin".
	Origin string
}

// Validate checks the fields every definition needs.
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

// mergeAgents combines definitions from several sources, later groups
// replacing earlier ones by name: built-in, then project, then user.
//
// A replacement is the WHOLE definition, not a field-level merge: a user
// file that re-declares `explore` but omits `disallowedTools` drops the
// read-only restriction the built-in had. Field-level merging is hard to
// predict, and a silent privilege change is worse than an explicit one.
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

// readOnlyDisallowed are the mutating tools the read-only agents (explore,
// plan) do not get. The prompts also say they are read-only; the prompt is
// a hint, the tool list is the law.
var readOnlyDisallowed = []string{"write", "edit", "bash"}

// builtinDefinitions returns the sub-agents that ship with codebot. A
// project or user file of the same name replaces one.
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

// Definitions loads the sub-agents available in cwd: the built-in ones,
// overridden by project (.codebot/agents/) and then user (~/.codebot/agents/)
// definitions. A broken file is logged and skipped rather than blocking
// startup. smallModel, when set, runs the built-in explore agent.
func Definitions(cwd, smallModel string) []AgentDefinition {
	builtin := builtinDefinitions(cwd)
	if smallModel != "" {
		for i := range builtin {
			if builtin[i].Name == "explore" {
				builtin[i].Model = smallModel
			}
		}
	}
	project, errs := loadAgentsDir(filepath.Join(cwd, config.ConfigDir, "agents"))
	logLoadErrors(errs)
	user, errs := loadAgentsDir(filepath.Join(config.UserConfigDir(), "agents"))
	logLoadErrors(errs)
	return mergeAgents(builtin, project, user)
}

func logLoadErrors(errs []error) {
	for _, err := range errs {
		log.Printf("agent load error: %v", err)
	}
}
