package agent

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/voocel/codebot/internal/config"
	cbteam "github.com/voocel/codebot/internal/team"
)

// AgentSource is the provenance of an AgentDefinition. It maps to a trust
// tier: built-in is code-controlled and trusted; project lives in version
// control (team-reviewed); user is per-machine and the least scrutinised.
//
// The source is consulted at tool-pool assembly time — see FilterToolsForAgent's
// IsBuiltIn flag — so adding a new source means deciding whether sub-agents
// loaded from it should be subject to customAgentDisallowed.
type AgentSource string

const (
	SourceBuiltin AgentSource = "builtin"
	SourceProject AgentSource = "project" // .codebot/agents/
	SourceUser    AgentSource = "user"    // ~/.codebot/agents/
)

const generalPurposeAgentName = "general-purpose"

// IsBuiltIn reports whether the source qualifies for the built-in trust tier
// in tool filtering. Today only SourceBuiltin does; SourceProject is kept
// out because a project agent file changes via PR review but executes on
// every collaborator's machine — different threat model from code that
// shipped with the binary.
func (s AgentSource) IsBuiltIn() bool {
	return s == SourceBuiltin
}

// AgentDefinition is codebot's spec layer for a sub-agent. It carries every
// piece of information needed to materialise a subagent.Config, plus
// provenance metadata used by callers, the UI, and `/agents` listings.
//
// Three places construct AgentDefinitions:
//
//  1. Built-in defaults (see builtinAgentDefinitions)
//  2. agent_loader.LoadAgentsDir reading .codebot/agents/*.md frontmatter
//  3. (Future) plugin contributions
//
// All three feed MergeAgents and then BuildConfig in turn.
type AgentDefinition struct {
	// Name is the unique identifier the LLM passes to the subagent tool.
	// Must be non-empty and globally unique after merging — MergeAgents
	// resolves name collisions by source precedence (user > project > builtin).
	Name string

	// Description is shown to the LLM in the subagent tool's schema enum
	// description. Treat it like a docstring: it tells the model WHEN to
	// invoke this agent, not what the agent does internally.
	Description string

	// SystemPrompt is the agent's system prompt. For built-ins it is a Go
	// string literal; for file-loaded agents it is the markdown body that
	// follows the frontmatter.
	SystemPrompt string

	// Tools is an allow-list of tool names. nil or {"*"} means "everything
	// the filter rules allow"; otherwise the listed names are intersected
	// with the filter output. Used by file-loaded agents to scope the
	// surface further than the global filter does.
	Tools []string

	// DisallowedTools is a per-agent deny-list, applied AFTER the global
	// rules. Mapped to FilterOpts.ExtraDisallowed at BuildConfig time.
	DisallowedTools []string

	// Model is "inherit" (use the parent's model), an explicit model name,
	// or empty (defaults to inherit).
	Model string

	// MaxTurns caps the agent's loop. Zero means use the subagent default.
	MaxTurns int

	// Background, when true, marks the agent as one that should run async
	// by default. The subagent tool can still override this per call via
	// its `background` parameter.
	Background bool

	// Isolation selects the teammate's filesystem sandbox. Empty or "shared"
	// (the default) runs the teammate in the leader's cwd, sharing the working
	// tree. "worktree" gives it a private git worktree so its writes cannot
	// clobber a peer editing the same files — see team.Isolation. Honoured
	// only for teammate spawns in a git repo; a no-op elsewhere.
	Isolation string

	// Provenance — set by the loader, read by tooling that wants to point
	// the user at a definition's origin (`/agents show explore` etc.).
	Source   AgentSource
	BaseDir  string // directory the agent was loaded from, or "builtin"
	Filename string // file path within BaseDir, or "" for built-ins
}

// Validate checks the structural invariants we can verify without the world
// — required fields present, tool-name strings non-empty, etc. Returns the
// first error encountered. Loader call sites must invoke this after parsing.
func (d *AgentDefinition) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("agent definition missing name (from %s)", d.origin())
	}
	if d.Description == "" {
		return fmt.Errorf("agent %q missing description (from %s)", d.Name, d.origin())
	}
	if d.SystemPrompt == "" {
		return fmt.Errorf("agent %q missing system prompt body (from %s)", d.Name, d.origin())
	}
	if slices.Contains(d.Tools, "") {
		return fmt.Errorf("agent %q has empty entry in tools list", d.Name)
	}
	if slices.Contains(d.DisallowedTools, "") {
		return fmt.Errorf("agent %q has empty entry in disallowedTools list", d.Name)
	}
	switch d.Isolation {
	case "", "shared", cbteam.WorktreeIsolation:
	default:
		return fmt.Errorf("agent %q has invalid isolation %q (want \"\", \"shared\", or \"worktree\")", d.Name, d.Isolation)
	}
	return nil
}

// origin returns a human-readable description of where the definition came
// from, suitable for error messages.
func (d *AgentDefinition) origin() string {
	if d.Filename != "" {
		return filepath.Join(d.BaseDir, d.Filename)
	}
	if d.BaseDir != "" {
		return d.BaseDir
	}
	return string(d.Source)
}

// MergeAgents combines definitions from multiple sources, with later groups
// overriding earlier ones by name. The expected call order is:
//
//	MergeAgents(builtin, project, user)
//
// so a user file overrides a project file overrides a built-in. This matches
// the trust-vs-customisability ordering: ship sensible defaults, let teams
// override per-project, let individuals override per-machine.
//
// IMPORTANT: override is WHOLE-DEFINITION replacement, not field-level merge.
// A user file that re-declares `explore` but omits `disallowedTools` will
// drop the read-only restriction the built-in version had. This is
// intentional — field-level merging is hard to predict ("did I inherit X
// from project or builtin?") and we'd rather force the user to be explicit
// than make them debug surprising privilege escalations. Document this in
// any user-facing agent-authoring guide.
//
// Empty names are skipped silently — Validate() should catch them upstream;
// MergeAgents is the wrong place to surface schema errors.
func MergeAgents(groups ...[]AgentDefinition) []AgentDefinition {
	byName := make(map[string]AgentDefinition)
	order := make([]string, 0)
	for _, group := range groups {
		for _, def := range group {
			if def.Name == "" {
				continue
			}
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

// readOnlyDisallowed are the mutating tools excluded from read-only agents
// (explore, plan). Listed once so adding a new mutating tool (e.g. a future
// `chmod` or `mv`) triggers a single review point.
//
// Note: this enforces capability at the FILTER layer. The system prompts
// also tell the model "you are read-only" — the prompt is hint, the filter
// is law.
var readOnlyDisallowed = []string{"write", "edit", "bash"}

// BuiltinDefinitions returns the three sub-agents that ship with codebot.
//
// They use the same AgentDefinition shape that file-loaded agents use, so
// the bootstrap pipeline can treat builtin / project / user agents
// uniformly. Two practical consequences:
//
//  1. A user can override `explore` by dropping their own
//     .codebot/agents/explore.md into the project. The MergeAgents step
//     picks up the later source by name.
//  2. Built-ins go through the same Validate() and BuildConfig() pipeline
//     as everything else, so a typo in the prompt loader can't produce a
//     different runtime shape from a typo in a Go literal.
//
// If you find yourself adding a fourth built-in, ask first whether it
// should be a built-in at all — a project-level agent in
// .codebot/agents/ is usually the more honest place for codebase-specific
// roles.
func BuiltinDefinitions(cwd string) []AgentDefinition {
	return []AgentDefinition{
		{
			Name:            "explore",
			Description:     "Fast codebase exploration agent. Use when you need to find files by patterns, search code for keywords, or answer questions about the codebase (e.g. 'how does authentication work?'). Read-only, no modifications.",
			SystemPrompt:    config.ExploreSubAgentPrompt(cwd),
			DisallowedTools: readOnlyDisallowed,
			MaxTurns:        20,
			Source:          SourceBuiltin,
			BaseDir:         "builtin",
		},
		{
			Name:            "plan",
			Description:     "Software architect. Explore code and design implementation strategies with step-by-step plans.",
			SystemPrompt:    config.PlanSubAgentPrompt(cwd),
			DisallowedTools: readOnlyDisallowed,
			MaxTurns:        25,
			Source:          SourceBuiltin,
			BaseDir:         "builtin",
		},
		{
			Name:         generalPurposeAgentName,
			Description:  "General-purpose coding agent. Independently search, read, and write code to complete subtasks.",
			SystemPrompt: config.GeneralPurposeSubAgentPrompt(cwd),
			MaxTurns:     30,
			Source:       SourceBuiltin,
			BaseDir:      "builtin",
		},
	}
}
