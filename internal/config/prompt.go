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
