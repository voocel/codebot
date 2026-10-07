package prompt

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/codebot/internal/infra/config"
)

const memoryMaxLines = 200

func Memory(cwd string) Part {
	content := loadMemory(config.MemoryFilePath(cwd))
	if content == "" {
		// The memory instructions promise MEMORY.md is always in context.
		// Without a placeholder the model tries to read a file that doesn't
		// exist until something is saved.
		content = "Your MEMORY.md is currently empty. When you save new memories, they will appear here."
	}
	return Part{Key: "memory", Title: "Memory", Body: "Contents of " + config.MemoryFilePath(cwd) +
		" (auto-memory, persists across conversations):\n\n" + content +
		"\n\nMemories reflect what was true when they were written. Before relying on one, verify that the files, functions, or flags it mentions still exist — a memory saying X exists is not the same as X existing now."}
}

func loadMemory(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	if len(lines) <= memoryMaxLines {
		return raw
	}
	return strings.Join(lines[:memoryMaxLines], "\n") + fmt.Sprintf("\n\n<!-- MEMORY.md has %d lines (limit: %d). "+
		"Only the first %d lines were loaded. Move detailed content into "+
		"separate topic files and keep MEMORY.md as a concise index. -->",
		len(lines), memoryMaxLines, memoryMaxLines)
}

func memoryInstructions(memoryDir string) string {
	return fmt.Sprintf(`## Auto memory

You have a persistent memory directory at `+"`%s`"+`. It already exists, so write to it directly. Its contents carry over to future conversations.

- `+"`MEMORY.md`"+` is the index and is loaded into every conversation. Keep it under %d lines, one line per topic file: `+"`- [Title](file.md) — hook`"+`.
- Topic files are not loaded automatically; open the ones whose index line matters for the task. Start each with frontmatter:

`+"```markdown"+`
---
name: short-kebab-case-slug
description: one specific line saying when this file is relevant
type: user | feedback | project | reference
---
`+"```"+`

  Types: `+"`user`"+` — who the user is and their preferences; `+"`feedback`"+` — corrections and confirmed approaches, with the reason; `+"`project`"+` — goals and constraints the code doesn't show; `+"`reference`"+` — pointers to external resources.
- Save when the user asks you to remember something, or when you learn something non-obvious that will matter again. Don't save what the repo already records (code structure, git history, AGENTS.md) or what only matters to the current task — if asked to, save the non-obvious part instead.
- Update an existing file rather than adding a duplicate. When a memory turns out to be wrong, or the user corrects something you took from memory, fix or delete it. Delete memories the user asks you to forget.`, memoryDir, memoryMaxLines)
}
