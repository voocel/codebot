package agent

import (
	"strings"

	"github.com/voocel/agentcore"
)

// Plan-mode reminders carry the planning workflow guidance and refresh the
// read-only contract on a cadence. Two shapes:
//
//   - First reminder in a session (no prior plan-mode reminder tag in
//     history): full form — emits the iterative-planning workflow guidance
//     (explore loop, asking-good-questions, plan-file-structure,
//     when-to-converge) plus a short contract refresher. Bypasses the
//     5-turn cadence so the model sees the workflow before its first
//     planning move. Mirrors CC's "always attach on first turn in plan
//     mode" rule (utils/attachments.ts:1196 — only throttle once an
//     attachment exists).
//   - Subsequent reminders: sparse form — just the read-only contract
//     refresher, throttled to TURNS_BETWEEN_REMINDERS=5
//     (utils/attachments.ts:259) so the rule stays salient as the
//     enter_plan_mode tool_result drifts toward the tail of context.
//
// The plan.Manager.Enter() return value carries the contract slice
// (MUST-NOT, plan path, end-of-turn rules) — that ships per Enter via
// tool_result. Workflow guidance lives only here so re-entering plan mode
// in the same session doesn't pile up duplicate guidance bytes in history.
const (
	planModeReminderTag           = "<plan-mode-reminder>"
	planModeExitReminderTag       = "<plan-mode-exit-reminder>"
	planModeTurnsBetweenReminders = 5
)

func planModeReminderForNextPrompt(msgs []agentcore.AgentMessage, planFilePath string) (key, reminder string, ok bool) {
	if planFilePath == "" {
		return "", "", false
	}

	turns, hasPrior := planModeTurnsSinceReminder(msgs)

	// First plan-mode reminder ever in this history: emit full guidance
	// immediately, no cadence throttle. The model needs the workflow
	// before its first planning move. The plan path itself rides in on
	// the contract (Enter() return value, already adjacent in history),
	// so the full reminder doesn't repeat it.
	if !hasPrior {
		return "plan_mode:active", buildPlanModeFullReminder(), true
	}

	if turns < planModeTurnsBetweenReminders {
		return "", "", false
	}
	return "plan_mode:active", buildPlanModeSparseReminder(planFilePath), true
}

func buildPlanModeSparseReminder(planFilePath string) string {
	var sb strings.Builder
	sb.WriteString("<system-reminder>\n")
	sb.WriteString(planModeReminderTag)
	sb.WriteString("\nPlan mode is still active. The only file you may modify is the plan file (")
	sb.WriteString(planFilePath)
	sb.WriteString(") — every other tool must remain read-only. End your turn with ask_user (to clarify) or exit_plan_mode (to request approval). Do NOT ask about plan approval via text or ask_user. Ignore this reminder if not relevant; never mention it to the user.\n</system-reminder>")
	return sb.String()
}

func buildPlanModeFullReminder() string {
	var sb strings.Builder
	sb.WriteString("<system-reminder>\n")
	sb.WriteString(planModeReminderTag)
	sb.WriteString("\n")
	sb.WriteString(planModeWorkflowGuidance)
	sb.WriteString("\nIgnore this reminder if not relevant; never mention it to the user.\n</system-reminder>")
	return sb.String()
}

// planModeWorkflowGuidance is the iterative-planning workflow text. Mirrors
// CC's getPlanModeInterviewInstructions (claude-code-src/utils/messages.ts).
// Delivered once per session via the first plan-mode reminder; re-reading
// these tips every Enter has no salience benefit.
const planModeWorkflowGuidance = `## Iterative Planning Workflow

You are pair-planning with the user. Explore the code to build context, ask the user questions when you hit decisions you can't make alone, and write your findings into the plan file as you go. The plan file is the ONLY file you may edit — it starts as a rough skeleton and gradually becomes the final plan.

### The Loop

Repeat this cycle until the plan is complete:

1. **Explore** — Use read, grep, glob, ls (and bash for read-only commands like ` + "`git status`" + `, ` + "`find`" + `, ` + "`cat`" + `, ` + "`sed -n`" + `) to read code. Look for existing functions, utilities, and patterns to reuse.
2. **Update the plan file** — After each discovery, immediately capture what you learned. Don't wait until the end.
3. **Ask the user** — When you hit an ambiguity or decision you can't resolve from code alone, use ask_user. Then go back to step 1.

### First Turn

Start by quickly scanning a few key files to form an initial understanding of the task scope. Then write a skeleton plan (headers and rough notes) and ask the user your first round of questions. Don't explore exhaustively before engaging the user.

### Asking Good Questions

- Never ask what you could find out by reading the code
- Batch related questions together (single ask_user call with multiple questions when applicable)
- Focus on things only the user can answer: requirements, preferences, tradeoffs, edge case priorities
- Scale depth to the task — a vague feature request needs many rounds; a focused bug fix may need one or none

### Plan File Structure

Your plan file should be divided into clear sections using markdown headers, based on the request. Fill out these sections as you go.
- Begin with a **Context** section: explain why this change is being made — the problem or need it addresses, what prompted it, and the intended outcome
- Include only your recommended approach, not all alternatives
- Ensure that the plan file is concise enough to scan quickly, but detailed enough to execute effectively
- Include the paths of critical files to be modified
- Reference existing functions and utilities you found that should be reused, with their file paths
- Include a verification section describing how to test the changes end-to-end (run the code, run tests, manual checks)

### When to Converge

Your plan is ready when you've addressed all ambiguities and it covers: what to change, which files to modify, what existing code to reuse (with file paths), and how to verify the changes. Call exit_plan_mode when the plan is ready for approval.
`

// planModeCancelledReminderForNextPrompt builds the one-shot reminder fired
// after /plan cancel. The EnterPlanMode tool_result (still in history) tells
// the model "MUST NOT make any edits, MUST stay read-only" — this reminder
// invalidates those rules so the model knows it can resume normal tool use.
// Mirrors CC's plan_mode_exit attachment text (refer/claude-code-src/utils/
// messages.ts:3854). exit_plan_mode already carries an equivalent message in
// its own tool_result, so this path is reserved for the slash-command cancel
// route which has no tool_result to ride on.
func planModeCancelledReminderForNextPrompt() (key, reminder string) {
	var sb strings.Builder
	sb.WriteString("<system-reminder>\n")
	sb.WriteString(planModeExitReminderTag)
	sb.WriteString("\nYou have exited plan mode. The read-only contract from the earlier enter_plan_mode tool result no longer applies — you may now make edits, run non-readonly tools, and take actions as normal. Ignore this reminder if not relevant; never mention it to the user.\n</system-reminder>")
	return "plan_mode:cancelled", sb.String()
}

// planModeTurnsSinceReminder counts assistant rounds since the last plan
// reminder injection. Returns (turns, hasPrior): hasPrior=false means no
// prior plan-mode reminder exists in history — the caller uses that to
// switch to the first-injection full-form path. Mirrors task_reminders'
// counting strategy so the two reminder cadences stay consistent within
// the same session.
func planModeTurnsSinceReminder(msgs []agentcore.AgentMessage) (turns int, hasPrior bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, ok := msgs[i].(agentcore.Message)
		if !ok {
			continue
		}
		switch msg.Role {
		case agentcore.RoleUser:
			if strings.Contains(msg.TextContent(), planModeReminderTag) {
				return turns, true
			}
		case agentcore.RoleAssistant:
			turns++
		}
	}
	return turns, false
}
