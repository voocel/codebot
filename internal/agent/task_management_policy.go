package agent

import (
	"strings"

	"github.com/voocel/agentcore"
	localtools "github.com/voocel/codebot/internal/tools"
)

const (
	taskManagementPromptReminder       = "<system-reminder>\nThis is a multi-step implementation task. Create and maintain a task list before going deeper. Break the work into concrete tasks, mark a task in_progress before starting it, mark it completed immediately after finishing it, and keep moving to the next unblocked task.\n</system-reminder>"
	taskManagementMissingReminder      = "<system-reminder>\nYou are doing multi-step work without maintaining a task list. Create concrete tasks now instead of continuing without structure.\n</system-reminder>"
	taskManagementExpandSingleReminder = "<system-reminder>\nYour task list is too broad for the current scope. Split the single broad task into multiple more specific tasks and keep their statuses up to date.\n</system-reminder>"
)

func taskManagementReminderForPrompt(blocks []agentcore.ContentBlock, snap localtools.TaskSnapshot) (string, bool) {
	if snap.Total > 0 && !allTasksCompleted(snap) {
		return "", false
	}

	text := strings.ToLower(strings.TrimSpace(textContentFromBlocks(blocks)))
	if text == "" || !looksLikeComplexTaskRequest(text) {
		return "", false
	}
	return taskManagementPromptReminder, true
}

func taskManagementReminderForTurn(turn TurnOutcomeSnapshot, snap localtools.TaskSnapshot) (key, reminder string, ok bool) {
	if turn.ReadOnlyToolCalls < 3 && turn.CodeEditToolCalls == 0 {
		return "", "", false
	}

	switch {
	case turn.TaskMutations == 0:
		return "task_management:missing", taskManagementMissingReminder, true
	case snap.Total == 1 && (turn.ReadOnlyToolCalls >= 3 || turn.CodeEditToolCalls > 0):
		return "task_management:expand_single", taskManagementExpandSingleReminder, true
	default:
		return "", "", false
	}
}

func textContentFromBlocks(blocks []agentcore.ContentBlock) string {
	var parts []string
	for _, block := range blocks {
		if strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func looksLikeComplexTaskRequest(text string) bool {
	if text == "" {
		return false
	}

	keywords := []string{
		"\u5b8c\u6574\u9879\u76ee", "\u5b8c\u6574\u7684\u9879\u76ee", "\u5b8c\u6574\u5e94\u7528", "\u5b8c\u6574\u7cfb\u7edf", "\u5b8c\u6574\u4ea7\u54c1",
		"cli\u5e94\u7528", "web\u5e94\u7528", "\u670d\u52a1\u7aef", "\u524d\u540e\u7aef", "\u811a\u624b\u67b6",
		"implement", "build", "create", "scaffold", "full project", "full app",
		"\u91cd\u6784", "\u642d\u5efa", "\u5f00\u53d1", "\u5b9e\u73b0", "\u8bbe\u8ba1\u5e76\u5b9e\u73b0", "\u4ece\u96f6\u5f00\u59cb",
	}
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}

	separators := 0
	for _, token := range []string{"\n", "\uff0c", ",", "\u3001", "\u4ee5\u53ca", " and ", " then "} {
		if strings.Contains(text, token) {
			separators++
		}
	}
	if separators >= 2 {
		return true
	}

	return len([]rune(text)) >= 60
}

func allTasksCompleted(snap localtools.TaskSnapshot) bool {
	return snap.Total > 0 && snap.Pending == 0 && snap.InProgress == 0
}
