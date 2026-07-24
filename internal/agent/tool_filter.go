package agent

import (
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/tools"
)

// Tool name constants used by the filter rules. Strings (not type-imports) are
// used here because tool packages would create an import cycle.
const (
	subagentToolName = "subagent"
	mcpToolPrefix    = "mcp__"
)

// allAgentDisallowed is the floor: no sub-agent — built-in or user-defined,
// sync or async — may call these. The reasoning per tool:
//
//   - ask_user: a sub-agent must not steal the user-facing dialogue channel;
//     the main agent owns the conversation.
//   - enter_plan_mode / exit_plan_mode: plan-mode arbitration is between the
//     main agent and the user; sub-agents have no standing.
//   - task_create / task_update / task_stop: the task list is the main
//     agent's coordination surface (see subagents.go for the design note);
//     sub-agents report results and the main agent updates state.
//   - cron_create / cron_delete: persistent scheduled jobs are a user-facing
//     concept; sub-agents may inspect via cron_list but not mutate.
//   - send_message: parent→child / peer-to-peer message delivery. The MAIN
//     agent and teammates both use it; one-shot subagents do not — they have
//     no peer surface and cannot inject into the leader's run safely.
//     Teammates DO get send_message added back via their force-injected tool
//     set in the runner (Stage D); for now it's all-disallowed at the filter
//     layer because no teammate spawn path exists yet.
//   - team_create: there is exactly one team per session, and only the main
//     agent (the leader) creates it. A sub-agent or teammate creating a
//     nested team would split the coordination surface in two.
//   - team_dismiss: only the leader gets to retire teammates. A teammate
//     dismissing peers would re-introduce the same coordination-surface
//     split team_create avoids.
//
// The `subagent` tool itself is always filtered out by FilterToolsForAgent
// regardless of this map — see the function for the recursion guard.
var allAgentDisallowed = map[string]bool{
	"ask_user":        true,
	"enter_plan_mode": true,
	"exit_plan_mode":  true,
	"task_create":     true,
	"task_update":     true,
	"task_stop":       true,
	"cron_create":     true,
	"cron_delete":     true,
	"send_message":    true,
	"team_create":     true,
	"team_dismiss":    true,
}

// customAgentDisallowed adds a stricter floor for sub-agents loaded from
// user-controlled sources (.codebot/agents/*.md, etc.). Empty today — the
// hook exists so Stage 3 can tighten file-write surfaces for user-defined
// agents without disturbing the built-in agents.
var customAgentDisallowed = map[string]bool{}

// asyncAgentAllowed is a strict allow-list. Async (background) sub-agents
// have no path to prompt the user, so we list precisely the tools that are
// safe to run without supervision.
//
// Skill and tool_search are deliberately omitted: at bootstrap time they are
// wired AFTER buildSubAgents runs (Skill needs subagentTool as its fork
// executor; tool_search wraps the final tool list), so the pool handed to a
// sub-agent never contains them. Listing them here would be a false promise.
var asyncAgentAllowed = map[string]bool{
	"read":       true,
	"write":      true,
	"edit":       true,
	"bash":       true,
	"glob":       true,
	"grep":       true,
	"ls":         true,
	"web_search": true,
	"web_fetch":  true,
}

// FilterOpts controls how FilterToolsForAgent selects tools for a sub-agent.
type FilterOpts struct {
	// IsBuiltIn is true when the agent is defined in code, false when loaded
	// from a user-controlled source (.codebot/agents/, plugins).
	IsBuiltIn bool

	// IsAsync is true for background-mode runs. Async agents cannot prompt
	// the user, so they are restricted to the asyncAgentAllowed list.
	IsAsync bool

	// AllowMCP controls whether MCP tools (mcp__*) pass through. Defaults
	// false — call sites that want MCP must opt in.
	AllowMCP bool

	// ExtraDisallowed is a per-agent denylist applied on top of the global
	// rules. Used by built-in agents to scope themselves further (e.g. the
	// explore agent excludes write/edit/bash to stay read-only).
	ExtraDisallowed []string
}

// FilterToolsForAgent returns the subset of `in` that a sub-agent may use.
//
// Checks per tool, in order:
//  1. The `subagent` tool itself is dropped (recursive spawn guard, applied
//     unconditionally — not configurable).
//  2. MCP tools (mcp__ prefix) pass through iff AllowMCP, bypassing the
//     allow/deny lists.
//  3. allAgentDisallowed is dropped.
//  4. customAgentDisallowed is dropped when !IsBuiltIn.
//  5. ExtraDisallowed (per-agent) is dropped.
//  6. When IsAsync, only asyncAgentAllowed passes; everything else is dropped.
func FilterToolsForAgent(in []agentcore.Tool, opts FilterOpts) []agentcore.Tool {
	extra := toSet(opts.ExtraDisallowed)
	out := make([]agentcore.Tool, 0, len(in))
	for _, t := range in {
		name := t.Name()
		if name == subagentToolName {
			continue
		}
		if strings.HasPrefix(name, mcpToolPrefix) {
			if opts.AllowMCP {
				out = append(out, t)
			}
			continue
		}
		if allAgentDisallowed[name] {
			continue
		}
		if !opts.IsBuiltIn && customAgentDisallowed[name] {
			continue
		}
		if extra[name] {
			continue
		}
		if opts.IsAsync && !asyncAgentAllowed[name] {
			continue
		}
		out = append(out, t)
	}
	return out
}

func toSet(xs []string) map[string]bool {
	if len(xs) == 0 {
		return nil
	}
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// BuildToolPool returns a tool list suitable as input to FilterToolsForAgent.
// Read / write / edit are replaced with fresh instances sharing a sub-agent-
// local FileReadState so a read in a sub-agent cannot poison the parent's
// read-stamp cache (which would let the parent's next write of the same file
// silently skip its own read-before-write check). Other tools are passed
// through by reference — they are either stateless or already keyed by cwd.
//
// Call this ONCE PER SUB-AGENT KIND. Two sub-agent kinds (e.g. explore and
// general-purpose) must NOT share the returned slice: doing so re-introduces
// the same cross-pollination of read state that we created this function to
// prevent.
// fs is the workspace backend the rebuilt read/write/edit instances operate
// on; nil falls back to the local filesystem (tools default to OSWorkspaceFS).
func BuildToolPool(cwd string, mainTools []agentcore.Tool, fs tools.WorkspaceFS) []agentcore.Tool {
	state := tools.NewFileReadState()
	out := make([]agentcore.Tool, 0, len(mainTools))
	for _, t := range mainTools {
		switch t.Name() {
		case "read":
			out = append(out, tools.NewRead(cwd, state, tools.WithFS(fs)))
		case "write":
			out = append(out, tools.NewWrite(cwd, state, tools.WithFS(fs)))
		case "edit":
			out = append(out, tools.NewEdit(cwd, state, tools.WithFS(fs)))
		default:
			out = append(out, t)
		}
	}
	return out
}
