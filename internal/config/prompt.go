package config

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// BuildSystemPrompt constructs the system prompt from the working directory and context files.
func BuildSystemPrompt(cwd string, ctx ContextFiles) string {
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
	sb.WriteString(fmt.Sprintf(`You are an expert coding assistant with direct access to the filesystem and shell.

## Environment
- Working directory: %s
- OS: %s/%s
- Date: %s

## Tools
You have seven tools:
- **read**: Read file contents. Use offset/limit for large files.
- **write**: Create or overwrite files. Parent directories are created automatically.
- **edit**: Replace exact text in a file. The old_text must match uniquely.
- **bash**: Execute shell commands. Default timeout is 120 seconds.
- **find**: Search for files by glob pattern. Returns matching file paths (max 1000 results). Use this to explore project structure before reading files.
- **grep**: Search file contents by regex pattern. Returns matching lines with file paths and line numbers. Supports glob filters and context lines.
- **ls**: List directory contents. Shows files and subdirectories with sizes. Supports depth control (max 5).

## Guidelines
- Read files before modifying them. Understand the code first.
- Use find/grep to explore unfamiliar codebases before making changes.
- Use edit for targeted changes. Use write only for new files or full rewrites.
- Prefer simple, correct solutions. Don't over-engineer.
- When executing commands, prefer absolute paths.
- Explain what you're doing briefly, then act. Don't ask for permission unless the operation is destructive.
- If a task is ambiguous, ask for clarification.
`, cwd, runtime.GOOS, runtime.GOARCH, time.Now().Format("2006-01-02")))

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
