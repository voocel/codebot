package agent

import (
	"strings"

	"github.com/voocel/agentcore"
)

// Plan-mode reminder periodically refreshes the read-only contract in long
// plan-mode sessions. The full plan-mode rules live in the system prompt
// overlay (set by plan.Manager via OverlayPrompt); this short reminder is
// injected as a system-reminder text block ahead of the next user message
// so the rule stays salient near the tail of context as the conversation
// grows. Mirrors CC's PLAN_MODE_ATTACHMENT_CONFIG.TURNS_BETWEEN_ATTACHMENTS
// = 5 cadence (utils/attachments.ts:259) but only emits the sparse form —
// the full instructions are already cache-resident in the system block, so
// re-injecting them every cycle would waste tokens for no salience gain.
const (
	planModeReminderTag              = "<plan-mode-reminder>"
	planModeTurnsBetweenReminders    = 5
)

func planModeReminderForNextPrompt(msgs []agentcore.AgentMessage, planFilePath string) (key, reminder string, ok bool) {
	if planFilePath == "" {
		return "", "", false
	}
	if planModeTurnsSinceReminder(msgs) < planModeTurnsBetweenReminders {
		return "", "", false
	}

	var sb strings.Builder
	sb.WriteString("<system-reminder>\n")
	sb.WriteString(planModeReminderTag)
	sb.WriteString("\nPlan mode is still active (full rules are in the system prompt). The only file you may modify is the plan file (")
	sb.WriteString(planFilePath)
	sb.WriteString(") — every other tool must remain read-only. End your turn with ask_user (to clarify) or exit_plan_mode (to request approval). Do NOT ask about plan approval via text or ask_user. Ignore this reminder if not relevant; never mention it to the user.\n</system-reminder>")
	return "plan_mode:active", sb.String(), true
}

// planModeTurnsSinceReminder counts assistant rounds since the last plan
// reminder injection. Mirrors task_reminders' counting strategy so the two
// reminder cadences stay consistent within the same session.
func planModeTurnsSinceReminder(msgs []agentcore.AgentMessage) int {
	turns := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, ok := msgs[i].(agentcore.Message)
		if !ok {
			continue
		}
		switch msg.Role {
		case agentcore.RoleUser:
			if strings.Contains(msg.TextContent(), planModeReminderTag) {
				return turns
			}
		case agentcore.RoleAssistant:
			turns++
		}
	}
	return turns
}
