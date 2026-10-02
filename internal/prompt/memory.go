package prompt

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/codebot/internal/config"
)

// memoryMaxLines is where LoadMemory truncates MEMORY.md, and the limit the
// memory instructions state.
const memoryMaxLines = 200

// LoadMemory returns the project's memory directory and the first 200 lines
// of its MEMORY.md, "" when there is none.
func LoadMemory(cwd string) (content, dir string) {
	dir = config.MemoryDir(cwd)
	path := config.MemoryFilePath(cwd)
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

// memoryInstructions teaches the model how to use auto memory.
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
