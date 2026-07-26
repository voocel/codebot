package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

func isolateUserConfigHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestIsSafeSummaryBoundary(t *testing.T) {
	t.Parallel()

	if isSafeSummaryBoundary(nil) {
		t.Fatal("empty conversation must not be declared safe")
	}

	userTurn := agentcore.UserMsg("hi")
	plainAssistant := agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{{Type: agentcore.ContentText, Text: "hello"}},
	}
	assistantWithToolUse := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			{Type: agentcore.ContentText, Text: "let me read that"},
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: "c1", Name: "Read"}),
		},
	}

	if !isSafeSummaryBoundary([]agentcore.AgentMessage{userTurn, plainAssistant}) {
		t.Fatal("text-only assistant tail must be safe to summarize up to")
	}
	if isSafeSummaryBoundary([]agentcore.AgentMessage{userTurn, assistantWithToolUse}) {
		t.Fatal("tool_use tail must postpone summarization until tool_result lands")
	}

	toolResult := agentcore.ToolResultMsg("c1", json.RawMessage(`"ok"`), false)
	if !isSafeSummaryBoundary([]agentcore.AgentMessage{userTurn, assistantWithToolUse, toolResult}) {
		t.Fatal("tool_result tail should be safe — the tool_use is no longer dangling")
	}
}

func TestStripCodeFence(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"plain body":                           "plain body",
		"```\nfenced body\n```":                "fenced body",
		"```markdown\n# Session Memory\n```":   "# Session Memory",
		"   ```\nleading whitespace body\n```": "leading whitespace body",
	}
	for in, want := range cases {
		if got := stripCodeFence(in); got != want {
			t.Errorf("stripCodeFence(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessionMemoryRoundtrip(t *testing.T) {
	isolateUserConfigHome(t)

	s := &Session{}
	dir := t.TempDir()
	s.cwd.Store(&dir)
	body := "# Session Memory\n\n## Current State\nPhase 4.1 in progress.\n"
	if err := s.saveSessionMemory(body); err != nil {
		t.Fatalf("save: %v", err)
	}
	mem, err := s.loadSessionMemory()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if mem == nil {
		t.Fatal("expected memory, got nil")
	}
	if !strings.Contains(mem.Content, "Phase 4.1 in progress") {
		t.Fatalf("content mismatch: %q", mem.Content)
	}
	if mem.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should reflect fs mtime")
	}
}

func TestLoadSessionMemoryMissing(t *testing.T) {
	isolateUserConfigHome(t)

	s := &Session{}
	dir := t.TempDir()
	s.cwd.Store(&dir)
	mem, err := s.loadSessionMemory()
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if mem != nil {
		t.Fatalf("missing file should yield nil memory, got %+v", mem)
	}
}

func TestSessionMemorySeedFnTemplateYieldsEmpty(t *testing.T) {
	isolateUserConfigHome(t)

	s := &Session{}
	dir := t.TempDir()
	s.cwd.Store(&dir)
	if err := s.saveSessionMemory(sessionMemoryTemplate); err != nil {
		t.Fatalf("save template: %v", err)
	}
	seed, err := SessionMemorySeedFn(s.currentCwd())()
	if err != nil {
		t.Fatalf("seed fn err: %v", err)
	}
	if seed != "" {
		t.Fatal("template-only memory must not trigger compaction — the pipeline should fall through to the LLM summary strategy")
	}
}

func TestSessionMemorySeedFnMissingYieldsEmpty(t *testing.T) {
	isolateUserConfigHome(t)

	seed, err := SessionMemorySeedFn(t.TempDir())()
	if err != nil {
		t.Fatalf("missing file must not error, got %v", err)
	}
	if seed != "" {
		t.Fatal("missing file must yield empty seed")
	}
}

func TestSessionMemorySeedFnReturnsBodyOnce(t *testing.T) {
	isolateUserConfigHome(t)

	s := &Session{}
	dir := t.TempDir()
	s.cwd.Store(&dir)
	body := "# Session Memory\n\n## Current State\nUser is wiring Phase 4.1-B.\n"
	if err := s.saveSessionMemory(body); err != nil {
		t.Fatalf("save: %v", err)
	}
	seed, err := SessionMemorySeedFn(s.currentCwd())()
	if err != nil {
		t.Fatalf("seed fn err: %v", err)
	}
	if !strings.Contains(seed, "User is wiring Phase 4.1-B") {
		t.Fatalf("expected memory body, got %q", seed)
	}
}
