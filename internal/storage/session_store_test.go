package storage

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func TestAppendAfterCloseReturnsError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s, err := create(dir, dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	err = s.AppendMessage(agentcore.Message{
		Role:    agentcore.RoleUser,
		Content: []agentcore.ContentBlock{agentcore.TextBlock("hello")},
	})
	if err == nil {
		t.Fatalf("expected append on closed store to fail")
	}
	if !strings.Contains(err.Error(), "session store is closed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTrimThinkingForStorage(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", maxStoredThinkingRunes+50)
	in := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ThinkingBlock(long),
			agentcore.TextBlock("visible answer"),
		},
	}
	out := trimThinkingForStorage(in)

	if &out.Content[0] == &in.Content[0] {
		t.Fatalf("expected a cloned content slice when trimming occurs")
	}
	if got := []rune(out.Content[0].Thinking); len(got) != maxStoredThinkingRunes {
		t.Fatalf("thinking length = %d, want %d", len(got), maxStoredThinkingRunes)
	}
	if !strings.HasSuffix(out.Content[0].Thinking, "…") {
		t.Fatalf("trimmed thinking should end with ellipsis, got %q", out.Content[0].Thinking)
	}
	if out.Content[1].Text != "visible answer" {
		t.Fatalf("non-thinking blocks must not be modified, got %q", out.Content[1].Text)
	}
	// Input must remain untouched.
	if len(in.Content[0].Thinking) != len(long) {
		t.Fatalf("input thinking was mutated")
	}
}

func TestTrimThinkingForStorageShortUnchanged(t *testing.T) {
	t.Parallel()

	in := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ThinkingBlock("short thought"),
		},
	}
	out := trimThinkingForStorage(in)
	if &out.Content[0] != &in.Content[0] {
		t.Fatalf("expected no-op to return input content slice as-is")
	}
}
