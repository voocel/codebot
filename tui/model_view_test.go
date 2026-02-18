package tui

import (
	"strings"
	"testing"
)

func TestViewShowsLiveThinkingWhenStreaming(t *testing.T) {
	m := New(nil, "test-model")
	m.Ready = true
	m.Width = 80
	m.IsStream = true
	m.Streaming.WriteString("assistant reply")
	m.Thinking.WriteString("thinking trace")

	view := m.View()
	if !strings.Contains(view, "[thinking]") {
		t.Fatalf("expected view to contain thinking label, got: %q", view)
	}
	if !strings.Contains(view, "[assistant]") {
		t.Fatalf("expected view to contain assistant label, got: %q", view)
	}
	if !strings.Contains(view, "assistant reply") {
		t.Fatalf("expected view to contain assistant streaming text, got: %q", view)
	}
}
