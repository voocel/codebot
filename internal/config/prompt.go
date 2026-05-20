package config

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
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

// BuildFrozenSystemParts returns the static portions of the system prompt
// that are fixed for the lifetime of the process: identity (cwd + OS) and
// the instructions block (hardcoded guidance + autoMemory + local tool
// directory + task-management section).
//
// Inputs MUST be process-stable. MCP tools, plan_mode overlays, and any
// runtime-mutable content belong in BuildDynamicSystemPart.
//
// When ctx.SystemOverride is set, identity is empty and frozenInstructions
// is the override verbatim.
func BuildFrozenSystemParts(cwd string, ctx ContextFiles, localTools []ToolInfo) (identity, frozenInstructions string) {
	if ctx.SystemOverride != "" {
		return "", ctx.SystemOverride
	}

	var identityBody strings.Builder
	// Date intentionally excluded from identity: it changes daily and would
	// invalidate the cached prompt prefix. It is surfaced via BuildReminders
	// as a per-turn <system-reminder> instead.
	fmt.Fprintf(&identityBody, `You are an expert coding assistant with direct access to the filesystem and shell.

## Environment
- Working directory: %s
- OS: %s/%s
`, cwd, runtime.GOOS, runtime.GOARCH)
	identity = identityBody.String()

	var toolsBody strings.Builder
	hasTaskCreate := false
	hasTaskUpdate := false
	hasTaskList := false
	if len(localTools) > 0 {
		fmt.Fprintf(&toolsBody, "## Tools\nYou have %d tools:\n", len(localTools))
		for _, t := range localTools {
			fmt.Fprintf(&toolsBody, "- **%s**: %s\n", t.Name, t.Description)
			switch t.Name {
			case "task_create":
				hasTaskCreate = true
			case "task_update":
				hasTaskUpdate = true
			case "task_list":
				hasTaskList = true
			}
		}
	}
	taskManagementInstructions := ""
	if hasTaskCreate && hasTaskUpdate && hasTaskList {
		taskManagementInstructions = `## Task Management
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
	}
	doingTasksInstructions := `## Doing tasks
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

	parallelExecutionInstructions := `## Parallel tool execution (CRITICAL)

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

	usingYourToolsInstructions := `## Using your tools
- Do NOT use bash to run commands when a relevant dedicated tool is provided. Using dedicated tools allows the user to better understand and review your work. This is CRITICAL to assisting the user:
  - To read files use read instead of cat, head, tail, or sed
  - To edit files use edit instead of sed or awk
  - To create files use write instead of cat with heredoc or echo redirection
  - To search for files use glob instead of find or ls
  - To search the content of files, use grep instead of grep or rg
  - Reserve using bash exclusively for system commands and terminal operations that require shell execution. If you are unsure and there is a relevant dedicated tool, default to using the dedicated tool and only fallback on using bash for these if it is absolutely necessary.`

	outputEfficiencyInstructions := `## Output efficiency

IMPORTANT: Go straight to the point. Try the simplest approach first without going in circles. Do not overdo it. Be extra concise.

Keep your text output brief and direct. Lead with the answer or action, not the reasoning. Skip filler words, preamble, and unnecessary transitions. Do not restate what the user said — just do it. When explaining, include only what is necessary for the user to understand.

Focus text output on:
- Decisions that need the user's input
- High-level status updates at natural milestones
- Errors or blockers that change the plan

If you can say it in one sentence, don't use three. Prefer short, direct sentences over long explanations. This does not apply to code or tool calls.`
	autoMemoryInstructions := BuildAutoMemoryInstructions(ctx.MemoryDir)
	var instructionParts []string
	if toolsBody.Len() > 0 {
		instructionParts = append(instructionParts, toolsBody.String())
	}
	instructionParts = append(instructionParts, parallelExecutionInstructions, doingTasksInstructions, usingYourToolsInstructions, outputEfficiencyInstructions)
	if taskManagementInstructions != "" {
		instructionParts = append(instructionParts, taskManagementInstructions)
	}
	if autoMemoryInstructions != "" {
		instructionParts = append(instructionParts, autoMemoryInstructions)
	}
	frozenInstructions = strings.Join(instructionParts, "\n\n")
	return
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

// BuildSystemBlockTexts is a backward-compatible wrapper. It splits tools by
// origin internally and concatenates frozen + dynamic into a single
// instructions string for callers that haven't migrated to the two-block
// layout yet.
//
// New code should call BuildFrozenSystemParts + BuildDynamicSystemPart
// directly so the static prefix can be cached independently.
func BuildSystemBlockTexts(cwd string, ctx ContextFiles, tools []ToolInfo) (identity, instructions string) {
	local, mcp := SplitToolsByOrigin(tools)
	id, frozen := BuildFrozenSystemParts(cwd, ctx, local)
	dyn := BuildDynamicSystemPart(mcp, nil)
	if dyn == "" {
		return id, frozen
	}
	if frozen == "" {
		return id, dyn
	}
	return id, frozen + "\n\n" + dyn
}

// BuildReminders extracts skills and context files into <system-reminder>
// wrapped text fragments for injection into user messages.
// Returns nil when there are no reminders to inject.
//
// Today's date is surfaced here (not in the system prompt) so that the
// system prefix stays identical across days and prompt cache can hit.
func BuildReminders(ctx ContextFiles, skills []skill.Spec) []string {
	reminders := []string{
		"<system-reminder>\nToday's date is " + time.Now().Format("2006-01-02") + ".\n</system-reminder>",
	}
	if skillBlock := skill.RenderListing(skills, skill.DefaultListingOptions()); skillBlock != "" {
		reminders = append(reminders, "<system-reminder>\n## Skills\n"+skillBlock+"\n</system-reminder>")
	}
	if ctx.Agents != "" {
		reminders = append(reminders, "<system-reminder>\n## Project Context\n"+ctx.Agents+"\n</system-reminder>")
	}
	if ctx.SystemAppend != "" {
		reminders = append(reminders, "<system-reminder>\n"+ctx.SystemAppend+"\n</system-reminder>")
	}
	if ctx.MemoryDir != "" {
		memPath := filepath.Join(ctx.MemoryDir, "MEMORY.md")
		body := ctx.Memory
		if body == "" {
			// The system prompt promises MEMORY.md is always loaded into
			// context. Without this placeholder the model sees the promise
			// and tries to Read the file itself, which surfaces ENOENT on
			// first session before any memory has been written.
			body = "Your MEMORY.md is currently empty. When you save new memories, they will appear here."
		}
		reminders = append(reminders, "<system-reminder>\nContents of "+memPath+" (auto-memory, persists across conversations):\n\n"+body+"\n</system-reminder>")
	}
	return reminders
}
