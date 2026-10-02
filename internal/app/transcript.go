package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/todo"
)

// HiddenToolCall reports whether a tool call is bookkeeping that frontends
// leave out of the transcript: todo_write, shown as the todo list instead,
// and reads of the auto memory, which hydrate the context like AGENTS.md.
func HiddenToolCall(tool string, args json.RawMessage) bool {
	switch tool {
	case todo.ToolName:
		return true
	case "read":
		var a struct {
			FilePath string `json:"file_path"`
		}
		_ = json.Unmarshal(args, &a)
		return isMemoryPath(a.FilePath)
	}
	return false
}

// isMemoryPath reports whether path lies in some project's memory directory
// (see config.MemoryDir).
func isMemoryPath(path string) bool {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, rest)
	}
	rel, err := filepath.Rel(filepath.Join(config.UserConfigDir(), "projects"), filepath.Clean(path))
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return len(parts) >= 3 && parts[0] != ".." && parts[1] == "memory"
}
