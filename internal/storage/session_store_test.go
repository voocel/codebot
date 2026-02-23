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
