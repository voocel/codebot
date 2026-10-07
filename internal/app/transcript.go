package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/agent/todo"
	"github.com/voocel/codebot/internal/infra/config"
)

// kindSkill marks a skill invoked as a command. The first block is the
// command as typed; the rest is the skill prompt.
const kindSkill = "skill"

// UserText returns what frontends show of a user message. ok is false for
// harness-added messages such as context or task notifications.
func UserText(m agentcore.Message) (text string, ok bool) {
	switch m.Kind {
	case "":
		return m.Text(), true
	case kindSkill:
		if len(m.Blocks) > 0 {
			if b, isText := m.Blocks[0].(litellm.TextBlock); isText {
				return b.Text, true
			}
		}
	}
	return "", false
}

// HiddenToolCall reports bookkeeping calls that frontends leave out of the
// transcript: todo_write, shown as the todo list, and auto-memory reads,
// which load context the way AGENTS.md does.
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

// isMemoryPath reports whether path is in any project's memory directory
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
