package agent

import (
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	localtools "github.com/voocel/codebot/internal/tools"
)

const (
	taskManagementMissingReminder      = "<system-reminder>\nYou are doing multi-step work without maintaining a task list. Create concrete tasks now instead of continuing without structure.\n</system-reminder>"
	taskManagementExpandSingleReminder = "<system-reminder>\nYour task list is too broad for the current scope. Split the single broad task into multiple more specific tasks and keep their statuses up to date.\n</system-reminder>"
)

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

func taskManagementReminderBeforeStop(msg agentcore.Message, snap localtools.TaskSnapshot) (key, reminder string, ok bool) {
	if msg.Role != agentcore.RoleAssistant || msg.StopReason != agentcore.StopReasonStop || snap.InProgress == 0 {
		return "", "", false
	}

	inProgress := inProgressTasks(snap)
	if len(inProgress) == 0 {
		return "", "", false
	}

	taskRefs := make([]string, 0, len(inProgress))
	taskIDs := make([]string, 0, len(inProgress))
	for _, task := range inProgress {
		taskIDs = append(taskIDs, task.ID)
		taskRefs = append(taskRefs, fmt.Sprintf("#%s %s", task.ID, task.Subject))
	}

	return "task_management:before_stop_open:" + strings.Join(taskIDs, ","),
		fmt.Sprintf(
			"<system-reminder>\nYou are about to stop with task(s) still in_progress: %s. If this work is actually finished, call task_update to mark it completed before your final answer. If it is blocked, partial, or still failing verification, keep the task in_progress and explicitly say that instead of ending as if the work were done.\n</system-reminder>",
			strings.Join(taskRefs, ", "),
		),
		true
}

func allTasksCompleted(snap localtools.TaskSnapshot) bool {
	return snap.Total > 0 && snap.Pending == 0 && snap.InProgress == 0
}

func inProgressTasks(snap localtools.TaskSnapshot) []localtools.Task {
	if len(snap.Items) == 0 || snap.InProgress == 0 {
		return nil
	}

	out := make([]localtools.Task, 0, snap.InProgress)
	for _, task := range snap.Items {
		if task.Status == localtools.TaskInProgress {
			out = append(out, task)
		}
	}
	return out
}
