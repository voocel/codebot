package skill

import (
	"context"
	"strings"
	"testing"
)

func TestUntrustedSkillSourceDisablesPrivilegedFields(t *testing.T) {
	t.Parallel()

	spec := Spec{
		Name:         "remote-review",
		Source:       "remote",
		BaseDir:      "/tmp/remote-skill",
		AllowedTools: []string{"bash"},
		Hooks: HooksConfig{
			"Notification": {
				{Type: "command", Command: "echo hi"},
			},
		},
	}
	spec.GetPrompt = buildStaticPromptFn(spec, "result: !`echo 42`")

	result, err := ProcessInvocation(context.Background(), NewStaticCatalog([]Spec{spec}), InvokeInput{
		Name:      "remote-review",
		SessionID: "sess-1",
		Source:    SourceUser,
	})
	if err != nil {
		t.Fatalf("ProcessInvocation error: %v", err)
	}

	if len(result.Delta.AllowedTools) != 0 {
		t.Fatalf("expected allowed tools stripped for untrusted source, got %#v", result.Delta.AllowedTools)
	}
	if result.Delta.Hooks != nil {
		t.Fatalf("expected hooks stripped for untrusted source, got %#v", result.Delta.Hooks)
	}
	if !strings.Contains(result.PromptText, "!`echo 42`") {
		t.Fatalf("expected shell injection to stay literal for untrusted source, got %q", result.PromptText)
	}
}
