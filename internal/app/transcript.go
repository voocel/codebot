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

// kindSkill marks the prompt of a skill the user invoked as a command. Its
// first block is the command as typed, the rest the skill.
const kindSkill = "skill"

// UserText is what frontends show of a user message: the user's words, or
// the command that invoked a skill. ok is false for a message the harness
// added, such as context or a task notification.
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
