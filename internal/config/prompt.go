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
// and available tools. When tools is non-empty the tool section is generated dynamically;
// otherwise a static fallback is used.
func BuildSystemPrompt(cwd string, ctx ContextFiles, tools []ToolInfo) string {
	// If SYSTEM.md exists, it replaces the entire default prompt.
	if ctx.SystemOverride != "" {
		prompt := ctx.SystemOverride
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

	sb.WriteString(`
## Guidelines
- Read files before modifying them. Understand the code first.
- Use find/grep to explore unfamiliar codebases before making changes.
- Use edit for targeted changes. Use write only for new files or full rewrites.
- Prefer simple, correct solutions. Don't over-engineer.
- When executing commands, prefer absolute paths.
- Explain what you're doing briefly, then act. Don't ask for permission unless the operation is destructive.
- If a task is ambiguous, ask for clarification.
`)

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
