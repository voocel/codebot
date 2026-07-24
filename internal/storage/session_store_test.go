package storage

import (
	"os"
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

func TestBuildSnapshotReadsMessageLargerThanScannerLimit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s, err := create(dir, dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	large := strings.Repeat("x", 2*1024*1024)
	if err := s.AppendMessage(agentcore.UserMsg(large)); err != nil {
		t.Fatalf("append large message: %v", err)
	}
	if err := s.AppendMessage(agentcore.UserMsg("after large message")); err != nil {
		t.Fatalf("append following message: %v", err)
	}

	snapshot, err := s.BuildSnapshot()
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if len(snapshot.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(snapshot.Messages))
	}
	if got := snapshot.Messages[0].TextContent(); got != large {
		t.Fatalf("large message length = %d, want %d", len(got), len(large))
	}
	if got := snapshot.Messages[1].TextContent(); got != "after large message" {
		t.Fatalf("following message = %q", got)
	}
}

func TestOpenIgnoresCrashTornFinalLine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s, err := create(dir, dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := s.AppendMessage(agentcore.UserMsg("before crash")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open session tail: %v", err)
	}
	if _, err := f.WriteString(`{"kind":"message","id":"torn"`); err != nil {
		_ = f.Close()
		t.Fatalf("write torn tail: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close torn tail: %v", err)
	}

	resumed, err := open(path)
	if err != nil {
		t.Fatalf("open after torn tail: %v", err)
	}
	defer resumed.Close()
	snapshot, err := resumed.BuildSnapshot()
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].TextContent() != "before crash" {
		t.Fatalf("unexpected recovered messages: %+v", snapshot.Messages)
	}
	if err := resumed.AppendMessage(agentcore.UserMsg("after recovery")); err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
	if err := resumed.Close(); err != nil {
		t.Fatalf("close resumed store: %v", err)
	}

	reopened, err := open(path)
	if err != nil {
		t.Fatalf("reopen after recovery append: %v", err)
	}
	defer reopened.Close()
	snapshot, err = reopened.BuildSnapshot()
	if err != nil {
		t.Fatalf("build reopened snapshot: %v", err)
	}
	if len(snapshot.Messages) != 2 || snapshot.Messages[1].TextContent() != "after recovery" {
		t.Fatalf("unexpected messages after recovery append: %+v", snapshot.Messages)
	}
}

func TestOpenRejectsMalformedCompleteLine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s, err := create(dir, dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open session tail: %v", err)
	}
	if _, err := f.WriteString("not-json\n"); err != nil {
		_ = f.Close()
		t.Fatalf("write malformed line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close malformed line: %v", err)
	}

	_, err = open(path)
	if err == nil {
		t.Fatal("expected malformed complete line to fail")
	}
	if !strings.Contains(err.Error(), "decode JSONL line 2") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenTerminatesValidFinalLineBeforeAppending(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s, err := create(dir, dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := s.AppendMessage(agentcore.UserMsg("complete without newline")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat session: %v", err)
	}
	if err := os.Truncate(path, info.Size()-1); err != nil {
		t.Fatalf("remove final newline: %v", err)
	}

	resumed, err := open(path)
	if err != nil {
		t.Fatalf("open unterminated valid line: %v", err)
	}
	if err := resumed.AppendMessage(agentcore.UserMsg("next message")); err != nil {
		_ = resumed.Close()
		t.Fatalf("append after unterminated line: %v", err)
	}
	if err := resumed.Close(); err != nil {
		t.Fatalf("close resumed store: %v", err)
	}

	reopened, err := open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	snapshot, err := reopened.BuildSnapshot()
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if len(snapshot.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(snapshot.Messages))
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
