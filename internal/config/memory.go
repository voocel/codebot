package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const memoryMaxLines = 200

// MemoryDir returns ~/.codebot/projects/<projectID>/memory/.
func MemoryDir(cwd string) string {
	return filepath.Join(UserConfigDir(), "projects", projectID(cwd), "memory")
}

// MemoryFilePath returns the path to MEMORY.md.
func MemoryFilePath(cwd string) string {
	return filepath.Join(MemoryDir(cwd), "MEMORY.md")
}

// LoadMemory reads MEMORY.md (first 200 lines) and returns the content
// along with the memory directory path. Returns empty strings when no
// memory file exists.
func LoadMemory(cwd string) (content, dir string) {
	dir = MemoryDir(cwd)
	path := filepath.Join(dir, "MEMORY.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", dir
	}

	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return "", dir
	}

	lines := strings.Split(raw, "\n")
	if len(lines) > memoryMaxLines {
		content = strings.Join(lines[:memoryMaxLines], "\n")
		content += fmt.Sprintf("\n\n<!-- MEMORY.md has %d lines (limit: %d). "+
			"Only the first %d lines were loaded. Move detailed content into "+
			"separate topic files and keep MEMORY.md as a concise index. -->",
			len(lines), memoryMaxLines, memoryMaxLines)
	} else {
		content = raw
	}
	return content, dir
}

// EnsureMemoryDir creates the memory directory if it doesn't exist.
func EnsureMemoryDir(cwd string) {
	_ = os.MkdirAll(MemoryDir(cwd), 0o755)
}

// BuildAutoMemoryInstructions returns the system prompt instructions that
// teach the LLM how to use auto memory. Returns empty string when memoryDir
// is empty (e.g. no user config dir).
func BuildAutoMemoryInstructions(memoryDir string) string {
	if memoryDir == "" {
		return ""
	}
	return fmt.Sprintf(`# Auto Memory

You have a persistent auto memory directory at `+"`%s`"+`. This directory already exists — write to it directly with the Write tool (do not run mkdir or check for its existence). Its contents persist across conversations.

As you work, consult your memory files to build on previous experience.

## How to save memories:
- Organize memory semantically by topic, not chronologically
- Use the Write and Edit tools to update your memory files
- `+"`MEMORY.md`"+` is always loaded into your conversation context — lines after 200 will be truncated, so keep it concise
- Create separate topic files (e.g., `+"`debugging.md`"+`, `+"`patterns.md`"+`) for detailed notes and link to them from MEMORY.md
- Update or remove memories that turn out to be wrong or outdated
- Do not write duplicate memories. First check if there is an existing memory you can update before writing a new one.

## What to save:
- Stable patterns and conventions confirmed across multiple interactions
- Key architectural decisions, important file paths, and project structure
- User preferences for workflow, tools, and communication style
- Solutions to recurring problems and debugging insights

## What NOT to save:
- Session-specific context (current task details, in-progress work, temporary state)
- Information that might be incomplete — verify against project docs before writing
- Anything that duplicates or contradicts existing AGENTS.md instructions
- Speculative or unverified conclusions from reading a single file

## Explicit user requests:
- When the user asks you to remember something across sessions (e.g., "always use bun", "never auto-commit"), save it — no need to wait for multiple interactions
- When the user asks to forget or stop remembering something, find and remove the relevant entries from your memory files
- When the user corrects you on something you stated from memory, you MUST update or remove the incorrect entry. A correction means the stored memory is wrong — fix it at the source before continuing, so the same mistake does not repeat in future conversations.`, memoryDir)
}
