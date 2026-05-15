package agent

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

// TestPlanModeFirstReminderEmitsFullGuidance locks the first-injection rule:
// when history carries no prior plan-mode reminder tag, the runtime emits the
// full-form reminder (workflow guidance + contract refresher) immediately,
// bypassing the 5-turn throttle. Mirrors CC's "always attach on first turn
// in plan mode" policy (utils/attachments.ts:1196).
func TestPlanModeFirstReminderEmitsFullGuidance(t *testing.T) {
	t.Parallel()

	planPath := "/tmp/plan.md"

	// No history at all — first injection.
	key, reminder, ok := planModeReminderForNextPrompt(nil, planPath)
	if !ok {
		t.Fatalf("expected first-injection reminder with empty history")
	}
	if key != "plan_mode:active" {
		t.Fatalf("unexpected key %q", key)
	}
	if !strings.Contains(reminder, planModeReminderTag) {
		t.Fatalf("first reminder must carry tag for cadence tracking, got %q", reminder)
	}
	// Workflow guidance markers must be present in the full form.
	for _, marker := range []string{"Iterative Planning Workflow", "The Loop", "Asking Good Questions", "When to Converge"} {
		if !strings.Contains(reminder, marker) {
			t.Fatalf("first reminder must include workflow guidance marker %q, got %q", marker, reminder)
		}
	}
	// The plan path lives in the contract (Enter() return value) which is
	// adjacent in history — full reminder must NOT re-state it. Re-stating
	// the path would also duplicate the contract anchor sentence and re-
	// introduce the ~30-token redundancy this split was meant to remove.
	if strings.Contains(reminder, planPath) {
		t.Fatalf("full reminder must not repeat plan path (lives in contract), got %q", reminder)
	}
	if strings.Contains(reminder, "is the only file you may edit; every other tool must remain read-only") {
		t.Fatalf("full reminder must not repeat the contract anchor sentence, got %q", reminder)
	}

	// History without prior plan-tag but with assistant turns — still first
	// injection, no cadence wait.
	msgs := []agentcore.AgentMessage{
		agentcore.Message{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("start")}},
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("ack")}},
	}
	if _, _, ok := planModeReminderForNextPrompt(msgs, planPath); !ok {
		t.Fatalf("first reminder must fire even with assistant history but no prior plan tag")
	}
}

// TestPlanModeReminderResetsAfterPriorInjection verifies the sparse cadence
// kicks in once the first reminder has been emitted: 5 assistant turns
// between repeats, and the form is sparse (no workflow guidance bytes).
func TestPlanModeReminderResetsAfterPriorInjection(t *testing.T) {
	t.Parallel()

	planPath := "/tmp/plan.md"

	// Build a history where a prior reminder was injected, followed by 4
	// assistant turns. Should NOT re-emit yet (need 5 since last).
	msgs := []agentcore.AgentMessage{
		agentcore.Message{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{
			agentcore.TextBlock("<system-reminder>\n" + planModeReminderTag + "\n...\n</system-reminder>"),
			agentcore.TextBlock("user content"),
		}},
	}
	for i := 0; i < 4; i++ {
		msgs = append(msgs, agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("ack")}})
		msgs = append(msgs, agentcore.Message{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("more")}})
	}
	if _, _, ok := planModeReminderForNextPrompt(msgs, planPath); ok {
		t.Fatalf("expected throttled reminder 4 turns after prior injection")
	}

	// 5th assistant turn since prior injection — emit again, sparse form.
	msgs = append(msgs, agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("ack")}})
	_, reminder, ok := planModeReminderForNextPrompt(msgs, planPath)
	if !ok {
		t.Fatalf("expected reminder 5 assistant turns after prior injection")
	}
	// Sparse form must NOT carry workflow guidance — that ships once on
	// first injection. Re-shipping it here is the regression we're guarding.
	if strings.Contains(reminder, "Iterative Planning Workflow") {
		t.Fatalf("subsequent reminders must be sparse (no workflow guidance), got %q", reminder)
	}
	if !strings.Contains(reminder, planPath) {
		t.Fatalf("sparse reminder must still carry plan path, got %q", reminder)
	}
}

func TestPlanModeCancelledReminderHasExitWording(t *testing.T) {
	t.Parallel()

	key, reminder := planModeCancelledReminderForNextPrompt()
	if key != "plan_mode:cancelled" {
		t.Fatalf("unexpected key %q", key)
	}
	if !strings.Contains(reminder, planModeExitReminderTag) {
		t.Fatalf("reminder must carry exit tag for dedupe, got %q", reminder)
	}
	if !strings.Contains(reminder, "exited plan mode") {
		t.Fatalf("reminder must announce plan-mode exit (mirrors CC messages.ts:3854), got %q", reminder)
	}
	// Critical: must revoke the MUST-NOT contract that the EnterPlanMode
	// tool_result still asserts in history. If this phrase regresses the
	// model may keep itself read-only after /plan cancel.
	if !strings.Contains(reminder, "no longer applies") && !strings.Contains(reminder, "may now make edits") {
		t.Fatalf("reminder must revoke read-only contract, got %q", reminder)
	}
}

func TestPlanModeReminderSuppressedWithoutPath(t *testing.T) {
	t.Parallel()

	msgs := make([]agentcore.AgentMessage, 0, 10)
	for i := 0; i < 10; i++ {
		msgs = append(msgs, agentcore.Message{Role: agentcore.RoleAssistant})
	}
	if _, _, ok := planModeReminderForNextPrompt(msgs, ""); ok {
		t.Fatalf("expected no reminder when plan path is empty")
	}
}
