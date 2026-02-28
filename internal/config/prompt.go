package config

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// ToolInfo describes a tool for system prompt generation.
// Decoupled from agentcore.Tool to avoid package dependency.
type ToolInfo struct {
	Name        string
	Description string
}

// BuildSystemPrompt constructs the system prompt from the working directory, context files,
// available tools, and loaded skills. When tools is non-empty the tool section is generated
// dynamically; otherwise a static fallback is used.
func BuildSystemPrompt(cwd string, ctx ContextFiles, tools []ToolInfo, skills []Skill) string {
	// If SYSTEM.md exists, it replaces the entire default prompt.
	if ctx.SystemOverride != "" {
		prompt := ctx.SystemOverride
		if skillBlock := FormatSkillsForPrompt(skills); skillBlock != "" && hasReadTool(tools) {
			prompt += "\n\n## Skills\n" + skillBlock
		}
		if ctx.Agents != "" {
			prompt += "\n\n## Project Context\n" + ctx.Agents
		}
		if ctx.SystemAppend != "" {
			prompt += "\n\n" + ctx.SystemAppend
		}
		return prompt
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `You are an expert coding assistant with direct access to the filesystem and shell.

## Environment
- Working directory: %s
- OS: %s/%s
- Date: %s
`, cwd, runtime.GOOS, runtime.GOARCH, time.Now().Format("2006-01-02"))

	// Tools section: dynamic when tools are provided, static fallback otherwise.
	if len(tools) > 0 {
		fmt.Fprintf(&sb, "\n## Tools\nYou have %d tools:\n", len(tools))
		for _, t := range tools {
			fmt.Fprintf(&sb, "- **%s**: %s\n", t.Name, t.Description)
		}
	} else {
		sb.WriteString(`
## Tools
- **read**: Read file contents. Use offset/limit for large files.
- **write**: Create or overwrite files. Parent directories are created automatically.
- **edit**: Replace exact text in a file. The old_text must match uniquely.
- **bash**: Execute shell commands. Default timeout is 120 seconds.
- **find**: Search for files by glob pattern. Returns matching file paths (max 1000 results).
- **grep**: Search file contents by regex pattern. Returns matching lines with file paths and line numbers.
- **ls**: List directory contents. Shows files and subdirectories with sizes.
`)
	}

	sb.WriteString("\n## Guidelines\n")
	if len(tools) > 0 {
		sb.WriteString(buildGuidelines(tools))
	} else {
		sb.WriteString(`- Read files before modifying them. Understand the code first.
- Use find/grep to explore unfamiliar codebases before making changes.
- Use edit for targeted changes. Use write only for new files or full rewrites.
- Prefer simple, correct solutions. Don't over-engineer.
- When executing commands, prefer absolute paths.
- Explain what you're doing briefly, then act. Don't ask for permission unless the operation is destructive.
- If a task is ambiguous, ask for clarification.`)
	}
	sb.WriteByte('\n')

	// Skills section: injected when read tool is available (or static fallback).
	if skillBlock := FormatSkillsForPrompt(skills); skillBlock != "" && (len(tools) == 0 || hasReadTool(tools)) {
		sb.WriteString("\n## Skills\n")
		sb.WriteString(skillBlock)
		sb.WriteString("\n")
	}

	if ctx.Agents != "" {
		sb.WriteString("\n## Project Context\n")
		sb.WriteString(ctx.Agents)
		sb.WriteString("\n")
	}

	if ctx.SystemAppend != "" {
		sb.WriteString("\n")
		sb.WriteString(ctx.SystemAppend)
		sb.WriteString("\n")
	}

	return sb.String()
}

// buildGuidelines generates guidelines based on the available tools.
// Only tool-specific guidelines are included when the corresponding tool is present.
func buildGuidelines(tools []ToolInfo) string {
	has := make(map[string]bool, len(tools))
	for _, t := range tools {
		has[t.Name] = true
	}

	var lines []string
	if has["write"] || has["edit"] {
		lines = append(lines, "- Read files before modifying them. Understand the code first.")
	}
	if has["find"] || has["grep"] {
		lines = append(lines, "- Use find/grep to explore unfamiliar codebases before making changes.")
	}
	if has["edit"] && has["write"] {
		lines = append(lines, "- Use edit for targeted changes. Use write only for new files or full rewrites.")
	}
	lines = append(lines, "- Prefer simple, correct solutions. Don't over-engineer.")
	if has["bash"] {
		lines = append(lines, "- When executing commands, prefer absolute paths.")
	}
	lines = append(lines,
		"- Explain what you're doing briefly, then act. Don't ask for permission unless the operation is destructive.",
		"- If a task is ambiguous, ask for clarification.",
	)
	if has["subagent"] {
		lines = append(lines,
			"- Use the subagent tool with specialized agents when the task matches the agent's description. Subagents protect the main context from excessive search results.",
			"- For simple, directed searches (a specific file/class/function), use read/grep/find directly.",
			"- For broader tasks — analyzing a project, understanding a codebase, tracing a feature across files, or any exploration that will clearly require more than 3 searches — you MUST use the subagent tool with agent=explore instead of doing it yourself.",
			"- Sub-agents cannot see conversation history — provide sufficient context in the task description.",
			"- Use the tasks array to launch multiple sub-agents in parallel when needed.",
			"- Set background=true for long-running tasks; you will receive a <task-notification> when it completes.",
		)
	}
	if has["enter_plan_mode"] {
		lines = append(lines,
			"- For non-trivial tasks (new features, multi-file changes, architectural decisions), proactively use enter_plan_mode to explore and plan before coding.",
			"- Skip plan mode for simple fixes, single-line changes, or when the user gives very specific instructions.",
			"- If the user says \"plan\", \"think through\", \"design first\", or similar — enter plan mode.",
		)
	}
	return strings.Join(lines, "\n")
}

func hasReadTool(tools []ToolInfo) bool {
	for _, t := range tools {
		if t.Name == "read" {
			return true
		}
	}
	return false
}
