package agent

import (
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/storage"
)

const (
	taskManagementStaleReminderTag    = "<task-management-stale-reminder>"
	taskManagementStaleReminderLead   = "The task tools haven't been used recently."
	taskReminderTurnsSinceWrite       = 10
	taskReminderTurnsBetweenReminders = 10
)

func taskManagementReminderForNextPrompt(msgs []agentcore.AgentMessage, snap storage.TaskSnapshot) (key, reminder string, ok bool) {
	if snap.Total == 0 {
		return "", "", false
	}

	turnsSinceWrite, turnsSinceReminder := taskManagementReminderTurnCounts(msgs)
	if turnsSinceWrite < taskReminderTurnsSinceWrite || turnsSinceReminder < taskReminderTurnsBetweenReminders {
		return "", "", false
	}

	var sb strings.Builder
	sb.WriteString("<system-reminder>\n")
	sb.WriteString(taskManagementStaleReminderTag)
	sb.WriteString("\n")
	sb.WriteString(taskManagementStaleReminderLead)
	sb.WriteString(" If tracking progress would help for the current work, consider using task_create to add tasks and task_update to keep statuses current (set in_progress when starting and completed when done). Ignore this if it is not relevant. Never mention this reminder to the user.")

	if snap.Total > 0 {
		sb.WriteString("\n\nCurrent tasks:\n")
		for _, task := range snap.Items {
			fmt.Fprintf(&sb, "#%s. [%s] %s\n", task.ID, task.Status, task.Subject)
		}
	}
	sb.WriteString("</system-reminder>")
	return "task_management:stale", sb.String(), true
}

func taskManagementReminderBeforeStop(msg agentcore.Message, snap storage.TaskSnapshot) (key, reminder string, ok bool) {
	if msg.Role != agentcore.RoleAssistant || msg.StopReason != agentcore.StopReasonStop {
		return "", "", false
	}

	inProgress := inProgressTasks(snap)
	if len(inProgress) > 0 {
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
	return "", "", false
}

func inProgressTasks(snap storage.TaskSnapshot) []storage.Task {
	if len(snap.Items) == 0 || snap.InProgress == 0 {
		return nil
	}

	out := make([]storage.Task, 0, snap.InProgress)
	for _, task := range snap.Items {
		if task.Status == storage.TaskInProgress {
			out = append(out, task)
		}
	}
	return out
}

func taskManagementReminderTurnCounts(msgs []agentcore.AgentMessage) (turnsSinceWrite, turnsSinceReminder int) {
	lastTaskManagement := false
	lastReminder := false

	for i := len(msgs) - 1; i >= 0; i-- {
		msg, ok := msgs[i].(agentcore.Message)
		if !ok {
			continue
		}

		switch msg.Role {
		case agentcore.RoleAssistant:
			if !lastTaskManagement && messageUsesTaskManagement(msg) {
				lastTaskManagement = true
			}
			if !lastTaskManagement {
				turnsSinceWrite++
			}
			if !lastReminder {
				turnsSinceReminder++
			}
		case agentcore.RoleUser:
			if !lastReminder && strings.Contains(msg.TextContent(), taskManagementStaleReminderTag) {
				lastReminder = true
			}
		}

		if lastTaskManagement && lastReminder {
			break
		}
	}

	return turnsSinceWrite, turnsSinceReminder
}

func messageUsesTaskManagement(msg agentcore.Message) bool {
	for _, call := range msg.ToolCalls() {
		if call.Name == "task_create" || call.Name == "task_update" {
			return true
		}
	}
	return false
}
