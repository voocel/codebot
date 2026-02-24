package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/storage"
)

type stubChatModel struct{}

func (m *stubChatModel) Generate(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{
		Message: agentcore.Message{
			Role:       agentcore.RoleAssistant,
			Content:    []agentcore.ContentBlock{agentcore.TextBlock("ok")},
			StopReason: agentcore.StopReasonStop,
		},
	}, nil
}

func (m *stubChatModel) GenerateStream(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{
		Type:       agentcore.StreamEventDone,
		Message:    agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("ok")}},
		StopReason: agentcore.StopReasonStop,
	}
	close(ch)
	return ch, nil
}

func (m *stubChatModel) SupportsTools() bool { return true }

func TestSwitchSessionKeepsCurrentStateOnModelRestoreFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := storage.NewManager(dir)
	current, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}
	t.Cleanup(func() { _ = current.Close() })

	target, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create target session: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })

	if err := target.AppendMessage(textMessage(agentcore.RoleUser, "hello")); err != nil {
		t.Fatalf("append target message: %v", err)
	}
	if err := target.AppendModelChange("openai", "bad-model"); err != nil {
		t.Fatalf("append target model change: %v", err)
	}

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:   ag,
		Store:   current,
		Manager: mgr,
		Settings: config.Resolved{
			DefaultProvider: "openai",
			DefaultModel:    "good-model",
			APIKey:          "k",
			ContextWindow:   128000,
			AutoCompaction:  false,
			MaxTurns:        30,
		},
		Cwd: dir,
		CreateModel: func(_ string, model string, _ string, _ string) (agentcore.ChatModel, error) {
			if model == "bad-model" {
				return nil, errors.New("model restore failed")
			}
			return &stubChatModel{}, nil
		},
	})
	t.Cleanup(s.Close)

	oldPath := s.store.Path()
	oldProvider := s.Provider()
	oldModel := s.ModelName()

	err = s.SwitchSession(target.Header().SessionID)
	if err == nil {
		t.Fatalf("expected switch failure")
	}
	if !strings.Contains(err.Error(), "restore model") {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := s.store.Path(); got != oldPath {
		t.Fatalf("store path changed after failed switch: got %s want %s", got, oldPath)
	}
	if got := s.Provider(); got != oldProvider {
		t.Fatalf("provider changed after failed switch: got %s want %s", got, oldProvider)
	}
	if got := s.ModelName(); got != oldModel {
		t.Fatalf("model changed after failed switch: got %s want %s", got, oldModel)
	}

	if err := s.store.AppendMessage(textMessage(agentcore.RoleUser, "still-writable")); err != nil {
		t.Fatalf("current store should remain writable after failed switch: %v", err)
	}
}

func TestSetModelKeepsStateWhenPersistFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := storage.NewManager(dir)
	store, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:   ag,
		Store:   store,
		Manager: mgr,
		Settings: config.Resolved{
			DefaultProvider: "openai",
			DefaultModel:    "good-model",
			APIKey:          "k",
			ContextWindow:   128000,
			AutoCompaction:  false,
			MaxTurns:        30,
		},
		Cwd: dir,
		CreateModel: func(_ string, _ string, _ string, _ string) (agentcore.ChatModel, error) {
			return &stubChatModel{}, nil
		},
	})
	t.Cleanup(s.Close)

	events := 0
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type == SEModelChanged {
			events++
		}
	})
	t.Cleanup(unsub)

	oldProvider := s.Provider()
	oldModel := s.ModelName()

	err = s.SetModel("openai", "new-model", "k")
	if err == nil {
		t.Fatalf("expected set model failure")
	}
	if !strings.Contains(err.Error(), "persist model change") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := s.Provider(); got != oldProvider {
		t.Fatalf("provider changed after failed set model: got %s want %s", got, oldProvider)
	}
	if got := s.ModelName(); got != oldModel {
		t.Fatalf("model changed after failed set model: got %s want %s", got, oldModel)
	}
	if events != 0 {
		t.Fatalf("expected no model-changed events on failure, got %d", events)
	}
}

func textMessage(role agentcore.Role, text string) agentcore.Message {
	return agentcore.Message{
		Role:    role,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(text)},
	}
}

// stubTool is a minimal agentcore.Tool for testing tool/prompt switching.
type stubTool struct {
	name string
	desc string
}

func (t *stubTool) Name() string                                                    { return t.name }
func (t *stubTool) Description() string                                             { return t.desc }
func (t *stubTool) Schema() map[string]any                                          { return nil }
func (t *stubTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) { return nil, nil }

func TestSetToolsRebuildsPrompt(t *testing.T) {
	t.Parallel()

	allTools := []agentcore.Tool{
		&stubTool{name: "read", desc: "Read file contents"},
		&stubTool{name: "write", desc: "Write file contents"},
		&stubTool{name: "edit", desc: "Edit file contents"},
		&stubTool{name: "bash", desc: "Execute shell commands"},
		&stubTool{name: "find", desc: "Find files by pattern"},
		&stubTool{name: "grep", desc: "Search file contents"},
		&stubTool{name: "ls", desc: "List directory contents"},
	}

	ag := agentcore.NewAgent(
		agentcore.WithModel(&stubChatModel{}),
		agentcore.WithTools(allTools...),
	)
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      "/tmp/test",
		Tools:    allTools,
	})
	t.Cleanup(s.Close)

	// Switch to read-only tools.
	readOnly := s.ToolsByName("read", "find", "grep", "ls")
	s.SetTools(readOnly...)

	prompt := ag.State().SystemPrompt
	if strings.Contains(prompt, "**write**") {
		t.Fatal("prompt should not contain write tool after switching to read-only")
	}
	if strings.Contains(prompt, "**edit**") {
		t.Fatal("prompt should not contain edit tool after switching to read-only")
	}
	if !strings.Contains(prompt, "**read**") {
		t.Fatal("prompt should contain read tool")
	}

	// Guidelines should omit write/edit-specific lines.
	if strings.Contains(prompt, "Use edit for targeted changes") {
		t.Fatal("prompt should not contain edit guideline in read-only mode")
	}
	if strings.Contains(prompt, "Read files before modifying them") {
		t.Fatal("prompt should not contain modification guideline in read-only mode")
	}
}

func TestRestoreAllToolsRebuildsPrompt(t *testing.T) {
	t.Parallel()

	allTools := []agentcore.Tool{
		&stubTool{name: "read", desc: "Read file contents"},
		&stubTool{name: "write", desc: "Write file contents"},
		&stubTool{name: "edit", desc: "Edit file contents"},
	}
	extra := &stubTool{name: "plan_mode", desc: "Enter plan mode"}

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}), agentcore.WithTools(allTools...))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      "/tmp/test",
		Tools:    allTools,
	})
	t.Cleanup(s.Close)

	s.RestoreAllTools(extra)

	prompt := ag.State().SystemPrompt
	if !strings.Contains(prompt, "**write**") {
		t.Fatal("prompt should contain write tool after restore")
	}
	if !strings.Contains(prompt, "**plan_mode**") {
		t.Fatal("prompt should contain extra plan_mode tool after restore")
	}
}

func TestSetSystemSuffixRebuildsPrompt(t *testing.T) {
	t.Parallel()

	tools := []agentcore.Tool{
		&stubTool{name: "read", desc: "Read file contents"},
	}

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}), agentcore.WithTools(tools...))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      "/tmp/test",
		Tools:    tools,
	})
	t.Cleanup(s.Close)

	s.SetSystemSuffix("[PLAN MODE]")

	prompt := ag.State().SystemPrompt
	if !strings.Contains(prompt, "[PLAN MODE]") {
		t.Fatal("prompt should contain suffix")
	}
	if !strings.Contains(prompt, "**read**") {
		t.Fatal("prompt should still contain tool descriptions with suffix")
	}

	// Clear suffix.
	s.SetSystemSuffix("")

	prompt = ag.State().SystemPrompt
	if strings.Contains(prompt, "[PLAN MODE]") {
		t.Fatal("prompt should not contain suffix after clearing")
	}
}
