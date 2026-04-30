package agent

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestPlanModeReminderEmitsAfterFiveAssistantTurns(t *testing.T) {
	t.Parallel()

	planPath := "/tmp/plan.md"

	// 0 assistant turns: too soon, no reminder.
	if _, _, ok := planModeReminderForNextPrompt(nil, planPath); ok {
		t.Fatalf("expected no reminder before any assistant turns")
	}

	msgs := make([]agentcore.AgentMessage, 0, 10)
	msgs = append(msgs, agentcore.Message{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("start")}})
	for i := 0; i < 4; i++ {
		msgs = append(msgs, agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("ack")}})
	}
	if _, _, ok := planModeReminderForNextPrompt(msgs, planPath); ok {
		t.Fatalf("expected no reminder at 4 assistant turns")
	}

	msgs = append(msgs, agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("ack")}})
	key, reminder, ok := planModeReminderForNextPrompt(msgs, planPath)
	if !ok {
		t.Fatalf("expected reminder at 5 assistant turns")
	}
	if key != "plan_mode:active" {
		t.Fatalf("unexpected key %q", key)
	}
	if !strings.Contains(reminder, planModeReminderTag) {
		t.Fatalf("expected tag in reminder, got %q", reminder)
	}
	if !strings.Contains(reminder, planPath) {
		t.Fatalf("expected plan path in reminder, got %q", reminder)
	}
}

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

	// 5th assistant turn since prior injection — emit again.
	msgs = append(msgs, agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("ack")}})
	if _, _, ok := planModeReminderForNextPrompt(msgs, planPath); !ok {
		t.Fatalf("expected reminder 5 assistant turns after prior injection")
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
