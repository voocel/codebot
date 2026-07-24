package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/agentcore"
)

func TestTranscriptStore_AppendThenLoadRoundTrips(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	s := NewTranscriptStore(dir)

	// Turn 1: user prompt + assistant reply.
	if err := s.Append("researcher", []agentcore.AgentMessage{
		agentcore.UserMsg("investigate the bug"),
		assistantMessage("looking into it"),
	}); err != nil {
		t.Fatalf("append turn 1: %v", err)
	}
	// Turn 2: another exchange, appended (must not rewrite turn 1).
	if err := s.Append("researcher", []agentcore.AgentMessage{
		agentcore.UserMsg("any progress?"),
		assistantMessage("found it at line 42"),
	}); err != nil {
		t.Fatalf("append turn 2: %v", err)
	}

	got, err := s.Load("researcher")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("loaded %d messages, want 4: %+v", len(got), got)
	}
	wantRoles := []agentcore.Role{agentcore.RoleUser, agentcore.RoleAssistant, agentcore.RoleUser, agentcore.RoleAssistant}
	wantText := []string{"investigate the bug", "looking into it", "any progress?", "found it at line 42"}
	for i := range got {
		if got[i].GetRole() != wantRoles[i] {
			t.Errorf("msg[%d] role = %v, want %v", i, got[i].GetRole(), wantRoles[i])
		}
		if got[i].TextContent() != wantText[i] {
			t.Errorf("msg[%d] text = %q, want %q", i, got[i].TextContent(), wantText[i])
		}
	}
}

func TestTranscriptStore_LoadMissingIsEmpty(t *testing.T) {
	s := NewTranscriptStore(t.TempDir())
	got, err := s.Load("nobody")
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if got != nil {
		t.Errorf("missing transcript should load as nil, got %+v", got)
	}
}

func TestTranscriptStore_SkipsTornTailLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	s := NewTranscriptStore(dir)
	if err := s.Append("researcher", []agentcore.AgentMessage{
		agentcore.UserMsg("hello"),
		assistantMessage("hi"),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Simulate a crash that left a half-written final line.
	f, err := os.OpenFile(s.path("researcher"), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = f.WriteString(`{"role":"assistant","content":[{"type":"text",`) // truncated JSON
	_ = f.Close()

	got, err := s.Load("researcher")
	if err != nil {
		t.Fatalf("load after torn tail: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("torn tail should be skipped, leaving 2 messages, got %d", len(got))
	}
	if err := s.Append("researcher", []agentcore.AgentMessage{
		agentcore.UserMsg("after recovery"),
	}); err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
	got, err = s.Load("researcher")
	if err != nil {
		t.Fatalf("reload after recovery append: %v", err)
	}
	if len(got) != 3 || got[2].TextContent() != "after recovery" {
		t.Fatalf("unexpected transcript after recovery append: %+v", got)
	}
}

func TestTranscriptStore_NoDirIsNoOp(t *testing.T) {
	s := NewTranscriptStore("")
	if err := s.Append("x", []agentcore.AgentMessage{agentcore.UserMsg("hi")}); err != nil {
		t.Errorf("append with no dir should be a silent no-op, got %v", err)
	}
	got, err := s.Load("x")
	if err != nil || got != nil {
		t.Errorf("load with no dir = (%v, %v), want (nil, nil)", got, err)
	}
}

// assistantMessage builds a minimal assistant Message for transcript tests.
func assistantMessage(text string) agentcore.AgentMessage {
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(text)},
		StopReason: agentcore.StopReasonStop,
	}
}
