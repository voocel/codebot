package config

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/voocel/codebot/internal/skill"
)

// ToolInfo describes a tool for system prompt generation.
// Decoupled from agentcore.Tool to avoid package dependency.
type ToolInfo struct {
	Name        string
	Description string
}

// mcpToolPrefix is the conventional prefix for MCP-sourced tools.
// Keep in sync with agent.mcpToolPrefix.
const mcpToolPrefix = "mcp__"

// IsMCPTool reports whether a tool name belongs to an MCP server.
func IsMCPTool(name string) bool {
	return strings.HasPrefix(name, mcpToolPrefix)
}

// SplitToolsByOrigin partitions tools into local (session-stable) and MCP
// (runtime-mutable) buckets so callers can route them to the right
// frozen/dynamic system block.
func SplitToolsByOrigin(all []ToolInfo) (local, mcp []ToolInfo) {
	for _, t := range all {
		if IsMCPTool(t.Name) {
			mcp = append(mcp, t)
		} else {
			local = append(local, t)
		}
	}
	return
}

// SystemPromptMode* are the host-recognized values for
// agentcore/subagent.Config.SystemPromptMode. The enum stays as untyped
// string constants because agentcore stores it as a string hint and codebot
// is the only consumer that interprets it.
const (
	// SystemPromptModeDefault (or "" / unset) means the teammate inherits
	// the host's universal base block and teammate addendum;
	// cfg.SystemPrompt is wrapped as "# Custom Agent Instructions" inside
	// the role block. Recommended path for new role-specific agents.
	SystemPromptModeDefault = "default"

	// SystemPromptModeReplace skips both the universal base and the
	// teammate addendum; cfg.SystemPrompt becomes the only SystemBlock.
	// Use when the agent definition is already a complete self-contained
	// prompt.
	SystemPromptModeReplace = "replace"

	// SystemPromptModeAppend builds the default composition and then
	// appends cfg.SystemPrompt at the end of the role block — rare, only
	// when an agent needs to add tail content on top of the default.
	SystemPromptModeAppend = "append"
)

// IsKnownSystemPromptMode reports whether s is one of the modes this package
// understands. Empty string is treated as known (it falls back to Default).
func IsKnownSystemPromptMode(s string) bool {
	switch s {
	case "", SystemPromptModeDefault, SystemPromptModeReplace, SystemPromptModeAppend:
		return true
	}
	return false
}

// --- Shared prompt sections -------------------------------------------------
//
// These string constants are agent-agnostic guidance baked into the universal
// base block. Hoisted to package-level so both BuildUniversalBase (the
// leader/teammate shared prefix) and any future role builder can reuse them
// without duplicating bytes. Changing any of them invalidates the prompt
// cache for the entire session — keep edits intentional.

const parallelExecutionInstructions = `## Parallel tool execution (CRITICAL)

When you need multiple pieces of information that do not depend on each other, you MUST dispatch the tool calls in the SAME response — not sequentially across turns.

- Default: parallel. Only go sequential when a later call genuinely needs the output of an earlier one.
- Exploring an unfamiliar area? Fire 2–4 reads/greps/globs together.
- Verifying a hypothesis? Check all the suspects in one shot.
- Failing to parallelize wastes turns, tokens, and cache warmth. It is a correctness issue, not a style preference.

Examples of calls that should be parallel:
- Reading several suspected files
- Running grep on multiple patterns
- Read + grep + ls on the same exploration step
- Running a build and checking git status

Only serialize when the next argument literally depends on the previous result (e.g. read a path returned by grep).`

const doingTasksInstructions = `## Doing tasks
- The user will primarily request you to perform software engineering tasks. These may include solving bugs, adding new functionality, refactoring code, explaining code, and more. When given an unclear or generic instruction, consider it in the context of these software engineering tasks and the current working directory. For example, if the user asks you to change "methodName" to snake case, do not reply with just "method_name", instead find the method in the code and modify the code.
- You are highly capable and often allow users to complete ambitious tasks that would otherwise be too complex or take too long. You should defer to user judgement about whether a task is too large to attempt.
- In general, do not propose changes to code you haven't read. If a user asks about or wants you to modify a file, read it first. Understand existing code before suggesting modifications.
- Do not create files unless they're absolutely necessary for achieving your goal. Generally prefer editing an existing file to creating a new one, as this prevents file bloat and builds on existing work more effectively.
- Avoid giving time estimates or predictions for how long tasks will take, whether for your own work or for users planning projects. Focus on what needs to be done, not how long it might take.
- If an approach fails, diagnose why before switching tactics—read the error, check your assumptions, try a focused fix. Don't retry the identical action blindly, but don't abandon a viable approach after a single failure either. Escalate to the user with ask_user only when you're genuinely stuck after investigation, not as a first response to friction.
- If the user denies a tool call and the reason isn't obvious, use ask_user to ask what they'd prefer instead — don't guess and don't retry the same call.
- Be careful not to introduce security vulnerabilities such as command injection, XSS, SQL injection, and other OWASP top 10 vulnerabilities. If you notice that you wrote insecure code, immediately fix it. Prioritize writing safe, secure, and correct code.
- Don't add features, refactor code, or make "improvements" beyond what was asked. A bug fix doesn't need surrounding code cleaned up. A simple feature doesn't need extra configurability. Don't add docstrings, comments, or type annotations to code you didn't change. Only add comments where the logic isn't self-evident.
- Don't add error handling, fallbacks, or validation for scenarios that can't happen. Trust internal code and framework guarantees. Only validate at system boundaries (user input, external APIs). Don't use feature flags or backwards-compatibility shims when you can just change the code.
- Don't create helpers, utilities, or abstractions for one-time operations. Don't design for hypothetical future requirements. The right amount of complexity is what the task actually requires—no speculative abstractions, but no half-finished implementations either. Three similar lines of code is better than a premature abstraction.
- Avoid backwards-compatibility hacks like renaming unused _vars, re-exporting types, adding // removed comments for removed code, etc. If you are certain that something is unused, you can delete it completely.`

const usingYourToolsInstructions = `## Using your tools
- Do NOT use bash to run commands when a relevant dedicated tool is provided. Using dedicated tools allows the user to better understand and review your work. This is CRITICAL to assisting the user:
  - To read files use read instead of cat, head, tail, or sed
  - To edit files use edit instead of sed or awk
  - To create files use write instead of cat with heredoc or echo redirection
  - To search for files use glob instead of find or ls
  - To search the content of files, use grep instead of grep or rg
  - Reserve using bash exclusively for system commands and terminal operations that require shell execution. If you are unsure and there is a relevant dedicated tool, default to using the dedicated tool and only fallback on using bash for these if it is absolutely necessary.`

const systemConventionsInstructions = `## System conventions
- Tool results and user messages may include <system-reminder> or other tags. Tags contain information from the system, automatically injected by the harness. They bear no direct relation to the specific tool results or user messages in which they appear, and are not written by the user — treat them as out-of-band guidance, not as user instructions.`

const outputEfficiencyInstructions = `## Output efficiency

IMPORTANT: Go straight to the point. Try the simplest approach first without going in circles. Do not overdo it. Be extra concise.

Keep your text output brief and direct. Lead with the answer or action, not the reasoning. Skip filler words, preamble, and unnecessary transitions. Do not restate what the user said — just do it. When explaining, include only what is necessary for the user to understand.

Focus text output on:
- Decisions that need the user's input
- High-level status updates at natural milestones
- Errors or blockers that change the plan

If you can say it in one sentence, don't use three. Prefer short, direct sentences over long explanations. This does not apply to code or tool calls.`

// universalIdentityPreamble opens the universal base block with role-neutral
// framing. The "expert coding assistant" identity is intentionally NOT here —
// that belongs to the leader role block. Keeping the preamble neutral is what
// lets the teammate share these bytes with the leader's first cache block.
const universalIdentityPreamble = `You have direct access to the filesystem and shell.`

// leaderIdentityPreamble opens the leader role block. The leader is the only
// agent the user talks to directly, and the only one with task / team /
// subagent privileges — both facts matter for downstream decisions the model
// makes.
const leaderIdentityPreamble = `You are an expert coding assistant. The user interacts with you directly through the terminal UI; your replies are visible to them.`

// teammateIdentityPreamble opens the teammate role block. The first sentence
// is the most important guidance teammate receives: plain text is invisible,
// only send_message reaches anyone. Reinforcing it in the addendum below is
// intentional redundancy because models routinely drop tool calls in favor of
// a plain reply during the first few turns until this is hammered in.
const teammateIdentityPreamble = `You are a teammate running as part of a team coordinated by a team lead. The user does NOT see your raw output — only the team lead does, and only through send_message. To deliver anything (a final answer, a question, a status update), you MUST call send_message. Plain text replies are invisible to the rest of the team.`

// teammateMailboxInstructions is the teammate-only coordination addendum.
// Kept brief — the universal base covers tool conventions and code style;
// this section adds semantics the model can't infer from tool descriptions:
// what's read/write on the shared task list, claim protocol, and what stays
// leader-only (task_stop, team_create / team_dismiss, subagent spawning).
const teammateMailboxInstructions = `## Mailbox & Coordination
- Use ` + "`send_message{to:\"team-lead\", message:\"...\"}`" + ` to deliver your reply, ask clarifying questions, or report a blocker.
- Use ` + "`send_message{to:\"<peer-name>\", message:\"...\"}`" + ` to coordinate with another teammate when needed.
- Each turn begins with one inbound message (from the team lead or a peer); you wake, do the work, and produce one final reply that lands as your turn's output.
- You share the team task list with the leader and peers: call task_list / task_get to see ongoing work; call task_create / task_update to register subtasks you discover or mark your progress. Do NOT use task_stop — stopping another agent's work is a leader concern.
- Claiming work: when the leader assigns you a task (you'll see it in task_list, often after a send_message hint), call ` + "`task_update{taskId, status:\"in_progress\"}`" + ` — the system stamps you as the owner automatically. Mark it ` + "`completed`" + ` only when the work is fully done. If a task already has an owner that isn't you, leave it alone unless the leader explicitly reassigned it.
- Auto-pull: when your mailbox is empty and the shared task list has an unowned, unblocked task, the runner claims one for you and feeds it directly as your next turn's input (the prompt starts with "Complete all open tasks. Start with task #N: …"). Treat it the same as any other assignment — call ` + "`task_update status:\"in_progress\"`" + ` (no-op on owner since you already own it) and proceed.
- You cannot spawn other teammates and cannot create or dismiss teams — those re-shape the coordination surface and are leader-only.`

// BuildUniversalBase returns the agent-agnostic head of the system prompt:
// neutral identity preamble, environment metadata, and the five shared
// conventions (parallel exec / doing tasks / using your tools / system
// conventions / output efficiency). Tool inventory is deliberately NOT here
// — leader and teammate have different tool sets, so listing tools in the
// shared prefix would break the byte-equality precondition for cross-agent
// prompt cache reuse.
//
// Inputs MUST be process-stable: cwd does not change inside a session, OS
// metadata is fixed at compile time. MCP tools and runtime overlays go in
// BuildDynamicSystemPart, never here.
//
// This is the first SystemBlock of both leader and teammate AgentContexts,
// both with cache_control="ephemeral". When the leader has run a few turns,
// Anthropic's server-side cache holds these bytes; a fresh teammate's first
// request hits the same key and reads from cache instead of paying full
// input-token cost.
// SessionDate is the calendar date baked into system block 1, frozen at first
// use. Freezing is what keeps BuildUniversalBase a pure function of its inputs:
// the leader renders block 1 at boot and a worktree teammate re-renders it at
// spawn (see team.teammateBaseBlocks), and the two must produce identical bytes
// or they cannot share the cached prefix. A live time.Now() here would silently
// split that cache for every teammate spawned after midnight.
//
// The cost is a stale date in a process that outlives a day; the agent corrects
// it with a one-shot reminder instead (see agent.queueDateChangeReminder).
var SessionDate = sync.OnceValue(func() string { return time.Now().Format("2006-01-02") })

func BuildUniversalBase(cwd string) string {
	var b strings.Builder
	b.WriteString(universalIdentityPreamble)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "## Environment\n- Working directory: %s\n- OS: %s/%s\n- Today's date: %s\n\n",
		cwd, runtime.GOOS, runtime.GOARCH, SessionDate())
	b.WriteString(parallelExecutionInstructions)
	b.WriteString("\n\n")
	b.WriteString(doingTasksInstructions)
	b.WriteString("\n\n")
	b.WriteString(usingYourToolsInstructions)
	b.WriteString("\n\n")
	b.WriteString(systemConventionsInstructions)
	b.WriteString("\n\n")
	b.WriteString(outputEfficiencyInstructions)
	return b.String()
}

// BuildLeaderRoleBlock returns the leader-specific portion of the system
// prompt: leader identity, the leader's own tool inventory, the optional
// Task Management section (gated on task_* tools being present), the optional
// Team coordination section (gated on team_*+send_message+subagent), the
// optional auto-memory hints, and the project-scoped context (skill listing,
// AGENTS.md, MEMORY.md, APPEND_SYSTEM.md).
//
// That last group used to ride along with every user message as
// <system-reminder> fragments, which put one copy per turn into history. It
// lives here instead because none of it changes mid-session — only an explicit
// Session.Reload (/memory edit, plugin reload) can move it, and that is rare
// enough to be worth one cache write. See tasks/todo.md for the admission rule.
//
// localTools should be the leader's session-stable tool set (caller already
// filtered out MCP via SplitToolsByOrigin). The list is rendered verbatim so
// pass it in the order callers want the model to see. skills should already be
// ranked by skill.OrderForPrompt; RenderListing re-sorts them into a
// time-independent order so this block stays byte-stable.
//
// Goes in the second SystemBlock with cache_control="ephemeral".
func BuildLeaderRoleBlock(ctx ContextFiles, localTools []ToolInfo, skills []skill.Spec) string {
	hasTaskCreate, hasTaskUpdate, hasTaskList := false, false, false
	hasTeamDismiss, hasSendMessage, hasSubAgent := false, false, false
	for _, t := range localTools {
		switch t.Name {
		case "task_create":
			hasTaskCreate = true
		case "task_update":
			hasTaskUpdate = true
		case "task_list":
			hasTaskList = true
		case "team_dismiss":
			hasTeamDismiss = true
		case "send_message":
			hasSendMessage = true
		case "subagent":
			hasSubAgent = true
		}
	}

	var parts []string
	parts = append(parts, leaderIdentityPreamble)
	if tools := renderToolList(localTools); tools != "" {
		parts = append(parts, tools)
	}
	if hasTaskCreate && hasTaskUpdate && hasTaskList {
		parts = append(parts, taskManagementInstructions)
	}
	if hasTeamDismiss && hasSendMessage && hasSubAgent {
		parts = append(parts, teamCoordinationInstructions)
	}
	if mem := BuildAutoMemoryInstructions(ctx.MemoryDir); mem != "" {
		parts = append(parts, mem)
	}
	parts = append(parts, buildProjectContext(ctx, skills)...)
	return strings.Join(parts, "\n\n")
}

// buildProjectContext renders the workspace-scoped sections of block 2:
// skill catalog, AGENTS.md, MEMORY.md, and APPEND_SYSTEM.md. Order is fixed so
// the block's bytes depend only on content, never on call order.
func buildProjectContext(ctx ContextFiles, skills []skill.Spec) []string {
	var parts []string
	if listing := skill.RenderListing(skills, skill.DefaultListingOptions()); listing != "" {
		parts = append(parts, "## Skills\n"+listing)
	}
	if ctx.Agents != "" {
		parts = append(parts, "## Project Context\n"+ctx.Agents)
	}
	if ctx.MemoryDir != "" {
		parts = append(parts, buildMemorySection(ctx))
	}
	if ctx.SystemAppend != "" {
		parts = append(parts, ctx.SystemAppend)
	}
	return parts
}

func buildMemorySection(ctx ContextFiles) string {
	body := ctx.Memory
	if body == "" {
		// The auto-memory instructions promise MEMORY.md is always in
		// context. Without this placeholder the model sees the promise and
		// tries to Read the file, which is ENOENT before anything is saved.
		body = "Your MEMORY.md is currently empty. When you save new memories, they will appear here."
	}
	return "## Memory\nContents of " + filepath.Join(ctx.MemoryDir, "MEMORY.md") +
		" (auto-memory, persists across conversations):\n\n" + body +
		"\n\nMemories reflect what was true when they were written. Before relying on one, verify that the files, functions, or flags it mentions still exist — a memory saying X exists is not the same as X existing now."
}

// BuildTeammateRoleBlock returns the teammate-specific portion of the system
// prompt: identity preamble, tool inventory, mailbox/coordination addendum,
// and (when set) the agent definition's custom prompt under
// "# Custom Agent Instructions". localTools is the effective tool set
// (Config.Tools + injected coordination tools); empty/nil omits the tool
// list. Lives in the second SystemBlock with cache_control="ephemeral".
func BuildTeammateRoleBlock(localTools []ToolInfo, agentRolePrompt string) string {
	var parts []string
	parts = append(parts, teammateIdentityPreamble)
	if tools := renderToolList(localTools); tools != "" {
		parts = append(parts, tools)
	}
	parts = append(parts, teammateMailboxInstructions)
	if agentRolePrompt != "" {
		// Leading "\n" + the outer Join("\n\n") yields three newlines
		// before the header — stable byte layout for prompt cache reuse.
		parts = append(parts, "\n# Custom Agent Instructions\n"+agentRolePrompt)
	}
	return strings.Join(parts, "\n\n")
}

// renderToolList returns a "## Tools" section listing each ToolInfo, or empty
// string when localTools is empty. Hoisted out of both role builders so the
// output format stays identical — diverging formats here would cause subtle
// cache misses if any future code shares fingerprints across roles.
func renderToolList(localTools []ToolInfo) string {
	if len(localTools) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Tools\nYou have %d tools:\n", len(localTools))
	for _, t := range localTools {
		fmt.Fprintf(&b, "- **%s**: %s\n", t.Name, t.Description)
	}
	// Trim trailing newline so join("\n\n") produces consistent spacing.
	return strings.TrimRight(b.String(), "\n")
}

// taskManagementInstructions is the leader-only section gated on the presence
// of task_create/task_update/task_list. Conditional because the LLM should not
// be told to use tools it does not have.
const taskManagementInstructions = `## Task Management
Break down and manage your work with task_create, task_update, and task_list.
- Use these tools proactively for non-trivial work with multiple steps or requirements
- Do not create a task list for a single trivial or purely conversational request
- Break larger requests into multiple specific tasks instead of one broad task
- Mark a task in_progress before starting it
- Mark a task completed immediately after fully finishing it
- IMPORTANT: always mark the current task completed before giving your final answer, unless the work is blocked, partial, or still failing verification
- Do not batch multiple completions together
- Keep at most one task in_progress at a time
- Check task_list before creating more tasks if a relevant task may already exist
- After completing a task, call task_list to find the next pending or unblocked task`

// teamCoordinationInstructions is the leader-only section gated on the four
// team-related tools. Partial sets describe a workflow the LLM cannot
// actually execute, so we keep this all-or-nothing.
const teamCoordinationInstructions = `## Team coordination
A default team is already active in this session and you are auto-registered as "team-lead". You can spawn long-lived peer agents (teammates) into it whenever the work benefits from parallel specialists you can hold a multi-turn conversation with.

- ONE team per session. Decide between three delegation shapes BEFORE spawning:
  - one-shot subagent (agent+task): isolated context, returns the final answer in this turn. Use for a single self-contained question.
  - background subagent (agent+task+background): same isolation, runs detached, you receive a follow-up when it completes. Use for fire-and-forget.
  - teammate (agent+task+name, team_name optional): persistent, addressable by name, can be re-prompted with send_message over many turns. Use only when you genuinely need ongoing collaboration — never for work a single subagent call can finish.
- Workflow: subagent{name,…} per teammate → send_message{to:name,…} to coordinate → team_dismiss{name} when a teammate is no longer needed. No team setup step is required.
- Dispatching shared work: create todos with task_create, then ` + "`task_update{taskId, owner:\"<teammate-name>\"}`" + ` to hand them out — the system drops an assignment notice into the teammate's mailbox automatically, no extra send_message needed. The teammate will pick it up at its next turn and call ` + "`task_update status:\"in_progress\"`" + ` (which auto-claims it). Use this for parallel work; for single-step asks send_message is still simpler.
- ` + "`team_create`" + ` is OPTIONAL and only useful BEFORE you spawn any teammates: it relabels the team (e.g. "auth-refactor") so logs and the UI chip read better. After the first teammate is spawned the rename is rejected.
- A teammate is "idle" when parked on its mailbox between turns. You can send_message to any teammate regardless of state — messages queue and deliver at their next turn boundary.
- Teammates cannot spawn other teammates and cannot create teams. Only you (the leader) can.
- When a teammate finishes its current turn, you receive its last reply as a <teammate-message teammate_id="…"> attachment in your prompt stream. Treat it like any other input and decide whether to follow up or move on.
- For a stuck teammate, use task_stop on its task ID (hard cancel); for an orderly retirement, use team_dismiss (graceful, no abrupt cut).`

// BuildFrozenSystemParts returns the two cached system blocks: block 1 is the
// agent-agnostic identity, block 2 the leader role block. Both stay fixed for
// the session unless the workspace root moves or a reload swaps context files
// in; MCP tools, plan_mode overlays, and anything else runtime-mutable belong
// in BuildDynamicSystemPart, which is deliberately outside the cache prefix.
//
// The teammate path composes block 1 from the same builder, so the leader and
// its teammates share a byte-identical cache prefix.
func BuildFrozenSystemParts(cwd string, ctx ContextFiles, localTools []ToolInfo, skills []skill.Spec) (identity, frozenInstructions string) {
	return BuildIdentity(cwd, ctx), BuildLeaderInstructions(ctx, localTools, skills)
}

// BuildIdentity returns system block 1 on its own, for callers that must
// recompute it without block 2 — a worktree enter/exit moves the working
// directory this block states. A SystemOverride replaces the whole prompt, so
// there is no identity block to build.
func BuildIdentity(cwd string, ctx ContextFiles) string {
	if ctx.SystemOverride != "" {
		return ""
	}
	return BuildUniversalBase(cwd)
}

// BuildLeaderInstructions returns system block 2 on its own: the SYSTEM.md
// override verbatim when present, otherwise the composed leader role block.
// Sessions call this to rebuild block 2 after a reload without touching
// block 1.
func BuildLeaderInstructions(ctx ContextFiles, localTools []ToolInfo, skills []skill.Spec) string {
	if ctx.SystemOverride != "" {
		return ctx.SystemOverride
	}
	return BuildLeaderRoleBlock(ctx, localTools, skills)
}

// BuildDynamicSystemPart assembles the runtime-mutable portion of the system
// prompt: late-arriving tool descriptions (MCP) and named overlays
// (plan_mode, mcp instructions, etc.).
//
// Returns "" when neither input contributes content; callers should then
// omit the third system block entirely.
//
// overlays must be passed in a deterministic order — the same content in a
// different order changes the hash and uselessly breaks any cache placed on
// this segment by the caller.
func BuildDynamicSystemPart(mcpTools []ToolInfo, overlays []string) string {
	var parts []string
	if len(mcpTools) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "## MCP Tools\nYou have %d additional tools from MCP servers:\n", len(mcpTools))
		for _, t := range mcpTools {
			fmt.Fprintf(&b, "- **%s**: %s\n", t.Name, t.Description)
		}
		parts = append(parts, b.String())
	}
	for _, o := range overlays {
		if o == "" {
			continue
		}
		parts = append(parts, o)
	}
	return strings.Join(parts, "\n\n")
}
