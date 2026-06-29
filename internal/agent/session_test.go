package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/codebot/internal/config"
	goalstate "github.com/voocel/codebot/internal/goal"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/storage"
	localtools "github.com/voocel/codebot/internal/tools"
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

type noEffortChatModel struct {
	stubChatModel
}

func (m *noEffortChatModel) Capabilities() llm.Capabilities {
	return llm.Capabilities{
		Thinking: llm.ThinkingCapabilities{
			Supported: llm.SupportYes,
			Disable:   llm.SupportYes,
		},
	}
}

type panicChatModel struct{}

func (m *panicChatModel) Generate(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	panic("stale generation should not continue agent")
}

func (m *panicChatModel) GenerateStream(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	panic("stale generation should not continue agent")
}

func (m *panicChatModel) SupportsTools() bool { return true }

type scriptedReminderModel struct {
	mu                    sync.Mutex
	callCount             int
	secondCallSawReminder bool
}

func (m *scriptedReminderModel) Generate(
	_ context.Context,
	msgs []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sawInjectedReminder := false
	for _, msg := range msgs {
		if msg.Role == agentcore.RoleUser && strings.Contains(msg.TextContent(), "<system-reminder>") {
			sawInjectedReminder = true
		}
		if msg.Role == agentcore.RoleUser && strings.Contains(msg.TextContent(), "repeatedly calling the same tool") {
			m.secondCallSawReminder = true
		}
	}

	m.callCount++
	if m.callCount == 1 && !sawInjectedReminder {
		return &agentcore.LLMResponse{
			Message: toolCallMessage(
				agentcore.ToolCall{ID: "tc1", Name: "read", Args: json.RawMessage(`{"file_path":"main.go"}`)},
				agentcore.ToolCall{ID: "tc2", Name: "read", Args: json.RawMessage(`{"file_path":"main.go"}`)},
				agentcore.ToolCall{ID: "tc3", Name: "read", Args: json.RawMessage(`{"file_path":"main.go"}`)},
				agentcore.ToolCall{ID: "tc4", Name: "read", Args: json.RawMessage(`{"file_path":"main.go"}`)},
			),
		}, nil
	}

	return &agentcore.LLMResponse{Message: assistantTextMessage("steered")}, nil
}

func (m *scriptedReminderModel) GenerateStream(
	ctx context.Context,
	msgs []agentcore.Message,
	tools []agentcore.ToolSpec,
	opts ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	resp, err := m.Generate(ctx, msgs, tools, opts...)
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{
		Type:       agentcore.StreamEventDone,
		Message:    resp.Message,
		StopReason: resp.Message.StopReason,
	}
	close(ch)
	return ch, nil
}

func (m *scriptedReminderModel) SupportsTools() bool { return true }

func (m *scriptedReminderModel) SawRepeatedToolReminder() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.secondCallSawReminder
}

type countingGoalModel struct {
	mu            sync.Mutex
	reminderRuns  int
	lastRunNumber int
}

func (m *countingGoalModel) Generate(
	_ context.Context,
	msgs []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sawGoalReminder := false
	for _, msg := range msgs {
		if msg.Role == agentcore.RoleUser && strings.Contains(msg.TextContent(), "Continue the explicit goal") {
			sawGoalReminder = true
		}
	}
	if sawGoalReminder {
		m.reminderRuns++
		m.lastRunNumber = m.reminderRuns
		return &agentcore.LLMResponse{Message: assistantTextMessage(fmt.Sprintf("goal run %d", m.reminderRuns))}, nil
	}
	return &agentcore.LLMResponse{Message: assistantTextMessage("idle")}, nil
}

func (m *countingGoalModel) GenerateStream(
	ctx context.Context,
	msgs []agentcore.Message,
	tools []agentcore.ToolSpec,
	opts ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	resp, err := m.Generate(ctx, msgs, tools, opts...)
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{
		Type:       agentcore.StreamEventDone,
		Message:    resp.Message,
		StopReason: resp.Message.StopReason,
	}
	close(ch)
	return ch, nil
}

func (m *countingGoalModel) SupportsTools() bool { return true }

func (m *countingGoalModel) ReminderRuns() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reminderRuns
}

type taskCompletionReminderModel struct {
	mu          sync.Mutex
	callCount   int
	sawReminder bool
	taskID      string
}

func (m *taskCompletionReminderModel) Generate(
	_ context.Context,
	msgs []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sawInjectedReminder := false
	for _, msg := range msgs {
		if msg.Role == agentcore.RoleUser && strings.Contains(msg.TextContent(), "still in_progress") {
			sawInjectedReminder = true
			m.sawReminder = true
		}
	}

	m.callCount++
	switch m.callCount {
	case 1:
		return &agentcore.LLMResponse{Message: assistantTextMessage("总结完毕")}, nil
	case 2:
		if !sawInjectedReminder {
			return &agentcore.LLMResponse{Message: assistantTextMessage("no reminder")}, nil
		}
		return &agentcore.LLMResponse{
			Message: toolCallMessage(agentcore.ToolCall{
				ID:   "task-update-1",
				Name: "task_update",
				Args: json.RawMessage(fmt.Sprintf(`{"taskId":%q,"status":"completed"}`, m.taskID)),
			}),
		}, nil
	default:
		return &agentcore.LLMResponse{Message: assistantTextMessage("已补上任务完成状态")}, nil
	}
}

func (m *taskCompletionReminderModel) GenerateStream(
	ctx context.Context,
	msgs []agentcore.Message,
	tools []agentcore.ToolSpec,
	opts ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	resp, err := m.Generate(ctx, msgs, tools, opts...)
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{
		Type:       agentcore.StreamEventDone,
		Message:    resp.Message,
		StopReason: resp.Message.StopReason,
	}
	close(ch)
	return ch, nil
}

func (m *taskCompletionReminderModel) SupportsTools() bool { return true }

func (m *taskCompletionReminderModel) SawReminder() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sawReminder
}

type namedChatModel struct {
	name string
}

func (m *namedChatModel) Generate(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{Message: assistantTextMessage(m.name)}, nil
}

func (m *namedChatModel) GenerateStream(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{
		Type:       agentcore.StreamEventDone,
		Message:    assistantTextMessage(m.name),
		StopReason: agentcore.StopReasonStop,
	}
	close(ch)
	return ch, nil
}

func (m *namedChatModel) SupportsTools() bool { return true }

type countingChatModel struct {
	mu        sync.Mutex
	callCount int
}

func (m *countingChatModel) Generate(
	_ context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	m.mu.Lock()
	m.callCount++
	m.mu.Unlock()
	return &agentcore.LLMResponse{Message: assistantTextMessage("counted")}, nil
}

func (m *countingChatModel) GenerateStream(
	ctx context.Context,
	msgs []agentcore.Message,
	tools []agentcore.ToolSpec,
	opts ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	resp, err := m.Generate(ctx, msgs, tools, opts...)
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{
		Type:       agentcore.StreamEventDone,
		Message:    resp.Message,
		StopReason: resp.Message.StopReason,
	}
	close(ch)
	return ch, nil
}

func (m *countingChatModel) SupportsTools() bool { return true }

func (m *countingChatModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

type captureChatModel struct {
	mu            sync.Mutex
	captured      []agentcore.Message
	capturedTools []agentcore.ToolSpec
}

func (m *captureChatModel) Generate(
	_ context.Context,
	msgs []agentcore.Message,
	tools []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	m.mu.Lock()
	m.captured = append([]agentcore.Message(nil), msgs...)
	m.capturedTools = append([]agentcore.ToolSpec(nil), tools...)
	m.mu.Unlock()
	return &agentcore.LLMResponse{Message: assistantTextMessage("ok")}, nil
}

func (m *captureChatModel) GenerateStream(
	ctx context.Context,
	msgs []agentcore.Message,
	tools []agentcore.ToolSpec,
	opts ...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	resp, err := m.Generate(ctx, msgs, tools, opts...)
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{
		Type:       agentcore.StreamEventDone,
		Message:    resp.Message,
		StopReason: resp.Message.StopReason,
	}
	close(ch)
	return ch, nil
}

func (m *captureChatModel) SupportsTools() bool { return true }

func (m *captureChatModel) Captured() []agentcore.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentcore.Message(nil), m.captured...)
}

func (m *captureChatModel) CapturedTools() []agentcore.ToolSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentcore.ToolSpec(nil), m.capturedTools...)
}

type stubExecTool struct {
	name string
}

func (t *stubExecTool) Name() string           { return t.name }
func (t *stubExecTool) Description() string    { return "stub exec tool" }
func (t *stubExecTool) Schema() map[string]any { return nil }
func (t *stubExecTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}

type stubTool struct {
	name string
	desc string
}

func (t *stubTool) Name() string           { return t.name }
func (t *stubTool) Description() string    { return t.desc }
func (t *stubTool) Schema() map[string]any { return nil }
func (t *stubTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func assistantTextMessage(text string) agentcore.Message {
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(text)},
		StopReason: agentcore.StopReasonStop,
	}
}

func toolCallMessage(calls ...agentcore.ToolCall) agentcore.Message {
	blocks := make([]agentcore.ContentBlock, len(calls))
	for i, call := range calls {
		blocks[i] = agentcore.ToolCallBlock(call)
	}
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    blocks,
		StopReason: agentcore.StopReasonToolUse,
	}
}

func textMessage(role agentcore.Role, text string) agentcore.Message {
	return agentcore.Message{
		Role:    role,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(text)},
	}
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not satisfied before timeout")
}

func newEventTestSession(t *testing.T) *Session {
	t.Helper()
	s := NewSession(SessionConfig{
		Agent:    agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Settings: config.Resolved{MaxTurns: 10},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)
	return s
}

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
			Provider: "openai",
			Model:    "good-model",
			Providers: map[string]config.ProviderConfig{
				"openai": {APIKey: "k"},
			},
			ContextWindow: 128000,

			MaxTurns: 30,
		},
		Cwd: dir,
		CreateModel: func(_ string, model string, _ string, _ string, _ map[string]any) (agentcore.ChatModel, error) {
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
}

func TestSwitchSessionRejectsUnsupportedReasoningEffort(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := storage.NewManager(dir)
	current, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}

	target, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create target session: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })
	if err := target.AppendReasoningEffortChange("high"); err != nil {
		t.Fatalf("append target reasoning effort: %v", err)
	}

	chatModel := &noEffortChatModel{}
	s := NewSession(SessionConfig{
		Agent:     agentcore.NewAgent(agentcore.WithModel(chatModel)),
		Store:     current,
		Manager:   mgr,
		Settings:  config.Resolved{Provider: "mimo", Model: "mimo-v2.5-pro", ContextWindow: 128000, MaxTurns: 30},
		Cwd:       dir,
		ChatModel: chatModel,
	})
	t.Cleanup(s.Close)

	err = s.SwitchSession(target.Header().SessionID)
	if err == nil {
		t.Fatal("SwitchSession succeeded with unsupported reasoning_effort")
	}
	if !strings.Contains(err.Error(), `unsupported reasoning_effort "high"`) {
		t.Fatalf("SwitchSession error = %v, want unsupported reasoning_effort", err)
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
			Provider: "openai",
			Model:    "good-model",
			Providers: map[string]config.ProviderConfig{
				"openai": {APIKey: "k"},
			},
			ContextWindow: 128000,

			MaxTurns: 30,
		},
		Cwd: dir,
		CreateModel: func(_ string, _ string, _ string, _ string, _ map[string]any) (agentcore.ChatModel, error) {
			return &stubChatModel{}, nil
		},
	})
	t.Cleanup(s.Close)

	oldProvider := s.Provider()
	oldModel := s.ModelName()

	err = s.SetModel("openai", "new-model")
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
}

func TestSetModelRejectsUnsupportedCurrentReasoningEffort(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := storage.NewManager(dir)
	store, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	s := NewSession(SessionConfig{
		Agent:   agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Store:   store,
		Manager: mgr,
		Settings: config.Resolved{
			Provider:        "openai",
			Model:           "base-model",
			ReasoningEffort: "high",
			Providers: map[string]config.ProviderConfig{
				"openai": {APIKey: "openai-key"},
			},
			ContextWindow: 128000,
			MaxTurns:      30,
		},
		Cwd: dir,
		CreateModel: func(_ string, _ string, _ string, _ string, _ map[string]any) (agentcore.ChatModel, error) {
			return &noEffortChatModel{}, nil
		},
		ChatModel: &stubChatModel{},
	})
	t.Cleanup(s.Close)

	err = s.SetModel("openai", "no-effort-model")
	if err == nil {
		t.Fatal("SetModel succeeded with unsupported current reasoning_effort")
	}
	if !strings.Contains(err.Error(), `unsupported reasoning_effort "high"`) {
		t.Fatalf("SetModel error = %v, want unsupported reasoning_effort", err)
	}
	if got := s.ModelName(); got != "base-model" {
		t.Fatalf("model changed after failed set model: got %q", got)
	}
	if got := s.Settings().ReasoningEffort; got != "high" {
		t.Fatalf("reasoning effort changed after failed set model: got %q", got)
	}
}

func TestSetModelDoesNotRewriteGlobalSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".codebot")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	initial := `{
  "provider": "openai",
  "model": "gpt-5.4",
  "small_model": "gpt-5.4",
  "providers": {
    "openai": {"api_key": "openai-key"},
    "anthropic": {"api_key": "anthropic-key"}
  }
}`
	settingsPath := filepath.Join(configDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	dir := t.TempDir()
	mgr := storage.NewManager(dir)
	store, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	s := NewSession(SessionConfig{
		Agent:   agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Store:   store,
		Manager: mgr,
		Settings: config.Resolved{
			Provider: "openai",
			Model:    "gpt-5.4",
			Providers: map[string]config.ProviderConfig{
				"openai":    {APIKey: "openai-key"},
				"anthropic": {APIKey: "anthropic-key"},
			},
			ContextWindow: 128000,

			MaxTurns: 30,
		},
		Cwd: dir,
		CreateModel: func(_ string, _ string, _ string, _ string, _ map[string]any) (agentcore.ChatModel, error) {
			return &stubChatModel{}, nil
		},
	})
	t.Cleanup(s.Close)

	// SetModel (internal) should NOT persist to global settings.
	if err := s.SetModel("anthropic", "claude-sonnet-4-5"); err != nil {
		t.Fatalf("set model: %v", err)
	}

	got, err := config.LoadSettingsStrict(dir)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if got.Provider != "openai" {
		t.Fatalf("provider rewritten: got %q want %q", got.Provider, "openai")
	}
	if got.Model != "gpt-5.4" {
		t.Fatalf("model rewritten: got %q want %q", got.Model, "gpt-5.4")
	}
	if s.Provider() != "anthropic" {
		t.Fatalf("session provider not switched: got %q want %q", s.Provider(), "anthropic")
	}
	if s.ModelName() != "claude-sonnet-4-5" {
		t.Fatalf("session model not switched: got %q want %q", s.ModelName(), "claude-sonnet-4-5")
	}
}

func TestSetReasoningEffortPersistsToProjectSettingsWhenPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	globalDir := filepath.Join(home, ".codebot")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global config dir: %v", err)
	}
	globalPath := filepath.Join(globalDir, "settings.json")
	if err := os.WriteFile(globalPath, []byte(`{"reasoning_effort":"low"}`), 0o600); err != nil {
		t.Fatalf("write global settings: %v", err)
	}

	cwd := t.TempDir()
	projectDir := filepath.Join(cwd, ".codebot")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project config dir: %v", err)
	}
	projectPath := filepath.Join(projectDir, "settings.json")
	if err := os.WriteFile(projectPath, []byte(`{"reasoning_effort":"high"}`), 0o600); err != nil {
		t.Fatalf("write project settings: %v", err)
	}

	s := NewSession(SessionConfig{
		Agent: agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Settings: config.Resolved{
			Provider:        "openai",
			Model:           "gpt-5",
			ReasoningEffort: "high",
			Providers: map[string]config.ProviderConfig{
				"openai": {APIKey: "openai-key"},
			},
			ContextWindow: 128000,
			MaxTurns:      30,
		},
		Cwd:       cwd,
		ChatModel: &stubChatModel{},
	})
	t.Cleanup(s.Close)

	s.SetThinkingLevel(agentcore.ThinkingAuto)

	projectRaw, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project settings: %v", err)
	}
	var projectSettings config.Settings
	if err := json.Unmarshal(projectRaw, &projectSettings); err != nil {
		t.Fatalf("decode project settings: %v", err)
	}
	if projectSettings.ReasoningEffort == nil || *projectSettings.ReasoningEffort != "" {
		t.Fatalf("project reasoning effort = %#v, want explicit auto/empty", projectSettings.ReasoningEffort)
	}
	globalSettings, err := config.LoadSettingsStrict(cwd)
	if err != nil {
		t.Fatalf("load merged settings: %v", err)
	}
	if globalSettings.Provider != "openai" {
		t.Fatalf("sanity provider = %q", globalSettings.Provider)
	}
	globalRaw, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("read global settings: %v", err)
	}
	if !strings.Contains(string(globalRaw), `"reasoning_effort":"low"`) && !strings.Contains(string(globalRaw), `"reasoning_effort": "low"`) {
		t.Fatalf("global settings was unexpectedly changed: %s", globalRaw)
	}
}

func TestSetThinkingLevelRejectsMinimal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := storage.NewManager(dir)
	store, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	s := NewSession(SessionConfig{
		Agent: agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Store: store,
		Settings: config.Resolved{
			Provider:        "openai",
			Model:           "gpt-5",
			ReasoningEffort: "low",
			Providers: map[string]config.ProviderConfig{
				"openai": {APIKey: "openai-key"},
			},
			ContextWindow: 128000,
			MaxTurns:      30,
		},
		Cwd:       dir,
		ChatModel: &stubChatModel{},
	})
	t.Cleanup(s.Close)

	var sawError bool
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type == SEError && ev.Error != nil && strings.Contains(ev.Error.Error(), `unsupported reasoning_effort "minimal"`) {
			sawError = true
		}
	})
	defer unsub()

	s.SetThinkingLevel(agentcore.ThinkingLevel("minimal"))
	if got := s.Settings().ReasoningEffort; got != "low" {
		t.Fatalf("reasoning effort = %q, want unchanged low", got)
	}
	if !sawError {
		t.Fatal("expected unsupported reasoning_effort error event")
	}
	snapshot, err := store.BuildSnapshot()
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if got := snapshot.ReasoningEffort; got != "" {
		t.Fatalf("persisted reasoning effort = %q, want none", got)
	}
}

func TestResetClearsRuntimeReasoningEffort(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(dir)
	store, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	s := NewSession(SessionConfig{
		Agent:   agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Store:   store,
		Manager: mgr,
		Settings: config.Resolved{
			Provider:        "openai",
			Model:           "gpt-5",
			ReasoningEffort: "high",
			Providers: map[string]config.ProviderConfig{
				"openai": {APIKey: "openai-key"},
			},
			ContextWindow: 128000,
			MaxTurns:      30,
		},
		Cwd:       dir,
		ChatModel: &stubChatModel{},
	})
	t.Cleanup(s.Close)

	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := s.Settings().ReasoningEffort; got != "" {
		t.Fatalf("reasoning effort after reset = %q, want auto/empty", got)
	}
}

func TestResolveCredentialsPerProvider(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent: ag,
		Settings: config.Resolved{
			Provider: "openai",
			Model:    "gpt-5",
			Providers: map[string]config.ProviderConfig{
				"openai":    {APIKey: "openai-key", BaseURL: "https://openai.example.com"},
				"anthropic": {APIKey: "ant-key"},
			},
		},
		Cwd: t.TempDir(),
	})
	t.Cleanup(s.Close)

	apiKey, baseURL := s.resolveCredentials("openai")
	if apiKey != "openai-key" || baseURL != "https://openai.example.com" {
		t.Fatalf("unexpected openai credentials: %s/%s", apiKey, baseURL)
	}
	apiKey, baseURL = s.resolveCredentials("anthropic")
	if apiKey != "ant-key" || baseURL != "" {
		t.Fatalf("unexpected anthropic credentials: %s/%s", apiKey, baseURL)
	}
}

func TestSetModelPassesProviderExtra(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := storage.NewManager(dir)
	store, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var gotExtra map[string]any
	s := NewSession(SessionConfig{
		Agent:   agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Store:   store,
		Manager: mgr,
		Settings: config.Resolved{
			Provider: "openai",
			Model:    "gpt-5",
			Providers: map[string]config.ProviderConfig{
				"anthropic-proxy": {
					Type:   "anthropic",
					APIKey: "proxy-key",
					Extra: map[string]any{
						"user_agent":     "claude-code/2.1.183",
						"anthropic_beta": "claude-code-20250219",
					},
				},
			},
		},
		Cwd: dir,
		CreateModel: func(_ string, _ string, _ string, _ string, extra map[string]any) (agentcore.ChatModel, error) {
			gotExtra = extra
			return &stubChatModel{}, nil
		},
	})
	t.Cleanup(s.Close)

	if err := s.SetModel("anthropic-proxy", "claude-sonnet-4-6"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if gotExtra["user_agent"] != "claude-code/2.1.183" {
		t.Fatalf("user_agent extra = %#v", gotExtra["user_agent"])
	}
	if gotExtra["anthropic_beta"] != "claude-code-20250219" {
		t.Fatalf("anthropic_beta extra = %#v", gotExtra["anthropic_beta"])
	}
}

func TestApplySkillInvocationUsesTemporaryOverrides(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	baseModel := &namedChatModel{name: "base-model"}
	s := NewSession(SessionConfig{
		Agent: agentcore.NewAgent(agentcore.WithModel(baseModel)),
		Settings: config.Resolved{
			Provider:        "openai",
			Model:           "base-model",
			ReasoningEffort: "low",
			ContextWindow:   128000,

			MaxTurns: 30,
			Providers: map[string]config.ProviderConfig{
				"openai": {APIKey: "k", Models: []string{"base-model", "skill-model"}},
			},
		},
		Cwd:       dir,
		ChatModel: baseModel,
		CreateModel: func(_ string, model string, _ string, _ string, _ map[string]any) (agentcore.ChatModel, error) {
			return &namedChatModel{name: model}, nil
		},
	})
	t.Cleanup(s.Close)

	err := s.ApplySkillInvocation(&skill.InvocationResult{
		Spec:       skill.Spec{Name: "review"},
		PromptText: "skill prompt body",
		Delta: skill.Delta{
			ModelOverride: "skill-model",
			Effort:        "high",
			Paths:         []string{"internal/skill/**"},
		},
	})
	if err != nil {
		t.Fatalf("ApplySkillInvocation error: %v", err)
	}
	if got := s.ModelName(); got != "skill-model" {
		t.Fatalf("temporary model = %q, want skill-model", got)
	}
	if got := s.Settings().ReasoningEffort; got != "high" {
		t.Fatalf("temporary reasoning effort = %q, want high", got)
	}

	s.clearSkillDelta()
	if got := s.ModelName(); got != "base-model" {
		t.Fatalf("restored model = %q, want base-model", got)
	}
}

func TestApplySkillInvocationQueuesPathHintsWithoutIdleAutoResume(t *testing.T) {
	t.Parallel()

	model := &countingChatModel{}
	ag := agentcore.NewAgent(agentcore.WithModel(model))
	if err := ag.SetMessages([]agentcore.AgentMessage{textMessage(agentcore.RoleAssistant, "previous answer")}); err != nil {
		t.Fatalf("SetMessages: %v", err)
	}

	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)

	err := s.ApplySkillInvocation(&skill.InvocationResult{
		Spec:       skill.Spec{Name: "review"},
		PromptText: "review prompt",
		Delta:      skill.Delta{Paths: []string{"internal/skill/**"}},
	})
	if err != nil {
		t.Fatalf("ApplySkillInvocation error: %v", err)
	}
	if got := model.Calls(); got != 0 {
		t.Fatalf("expected no idle auto-resume for path hints, got %d model calls", got)
	}
}

func TestInjectInvokedSkillContextAddsPreservedReminder(t *testing.T) {
	t.Parallel()

	s := NewSession(SessionConfig{
		Agent:    agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)

	s.recordInvokedSkill("review", "Investigate the diff carefully.", []string{"internal/skill/**"})
	msgs := []agentcore.AgentMessage{
		agentctx.ContextSummary{Summary: "summary"},
		textMessage(agentcore.RoleUser, "latest user message"),
	}
	result := s.injectInvokedSkillContext(msgs)
	if len(result) != 3 {
		t.Fatalf("expected preserved reminder inserted, got %d messages", len(result))
	}
	msg, ok := result[1].(agentcore.Message)
	if !ok || msg.Metadata["skill_preserve"] != true {
		t.Fatalf("unexpected inserted reminder: %#v", result[1])
	}
}

func TestPromptOverlaysAppendInSortedKeyOrder(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      t.TempDir(),
		Tools: []agentcore.Tool{
			&stubTool{name: "read", desc: "read file"},
		},
	})
	t.Cleanup(s.Close)

	// Register overlays in arbitrary order — output must be sorted by key
	// so byte-level rebuilds are stable across insertion sequences.
	s.OverlayPrompt("plan.mode", "plan overlay")
	s.SetMCPInstructions("mcp overlay")
	s.OverlayPrompt("plan.approved", "approved overlay")

	systemPrompt := ag.State().SystemPrompt
	mcpIdx := strings.Index(systemPrompt, "mcp overlay")
	planIdx := strings.Index(systemPrompt, "plan overlay")
	approvedIdx := strings.Index(systemPrompt, "approved overlay")
	if mcpIdx < 0 || planIdx < 0 || approvedIdx < 0 {
		t.Fatalf("missing overlays in system prompt: %q", systemPrompt)
	}
	// Sorted by key: "mcp" < "plan.approved" < "plan.mode".
	if !(mcpIdx < approvedIdx && approvedIdx < planIdx) {
		t.Fatalf("unexpected overlay order (want mcp < plan.approved < plan.mode): %q", systemPrompt)
	}
}

func TestSetToolsRebuildsPrompt(t *testing.T) {
	t.Parallel()

	allTools := []agentcore.Tool{
		&stubTool{name: "read", desc: "Read file contents"},
		&stubTool{name: "write", desc: "Write file contents"},
		&stubTool{name: "edit", desc: "Edit file contents"},
		&stubTool{name: "bash", desc: "Execute shell commands"},
		&stubTool{name: "glob", desc: "Match files by glob pattern"},
		&stubTool{name: "grep", desc: "Search file contents"},
		&stubTool{name: "ls", desc: "List directory contents"},
	}

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}), agentcore.WithTools(allTools...))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      "/tmp/test",
		Tools:    allTools,
	})
	t.Cleanup(s.Close)

	s.SetTools(s.ToolsByName("read", "glob", "grep", "ls")...)
	prompt := ag.State().SystemPrompt
	if strings.Contains(prompt, "**write**") || strings.Contains(prompt, "**edit**") {
		t.Fatal("prompt should not contain write/edit tool after switching to read-only")
	}
}

func TestReplaceMCPToolsUpdatesAllToolsWithoutBreakingFilteredActiveTools(t *testing.T) {
	t.Parallel()

	allTools := []agentcore.Tool{
		&stubTool{name: "read", desc: "Read file contents"},
		&stubTool{name: "write", desc: "Write file contents"},
		&stubTool{name: "mcp__docs__search", desc: "Search docs"},
	}

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}), agentcore.WithTools(allTools...))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      t.TempDir(),
		Tools:    allTools,
	})
	t.Cleanup(s.Close)

	s.SetTools(s.ToolsByName("read")...)
	s.ReplaceMCPTools([]agentcore.Tool{
		&stubTool{name: "mcp__ops__deploy", desc: "Deploy service"},
	})

	if got := len(s.ToolsByName("mcp__ops__deploy")); got != 1 {
		t.Fatalf("expected refreshed MCP tool in all tools, got %d", got)
	}
	if got := len(s.ToolsByName("mcp__docs__search")); got != 0 {
		t.Fatalf("expected stale MCP tool removed from all tools, got %d", got)
	}
	if len(s.activeTools) != 1 || s.activeTools[0].Name() != "read" {
		t.Fatalf("expected filtered active tools to remain unchanged, got %#v", s.activeTools)
	}
}

func TestBuildUserMessagePrependsRuntimeRemindersBeforeStaticReminders(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:     ag,
		Settings:  config.Resolved{MaxTurns: 30},
		Cwd:       t.TempDir(),
		Reminders: []string{"<system-reminder>\nstatic reminder\n</system-reminder>"},
	})
	t.Cleanup(s.Close)

	s.queueRuntimeReminder("loop", ReminderRepeatToolCall, "<system-reminder>\nruntime reminder\n</system-reminder>")
	msg := s.buildUserMessage(agentcore.TextBlock("user input"))
	if len(msg.Content) != 3 {
		t.Fatalf("expected 3 content blocks, got %d", len(msg.Content))
	}
	if !strings.Contains(msg.Content[0].Text, "runtime reminder") || !strings.Contains(msg.Content[1].Text, "static reminder") {
		t.Fatalf("unexpected content ordering: %#v", msg.Content)
	}
}

func TestContinueIfCurrentGenerationSkipsStaleSession(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&panicChatModel{}))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)

	s.mu.Lock()
	gen := s.generation
	s.generation++
	s.mu.Unlock()

	err := s.continueIfCurrentGeneration(gen)
	if !errors.Is(err, errStaleSessionGeneration) {
		t.Fatalf("expected stale generation error, got %v", err)
	}
}

func TestRepeatedToolCallQueuesRuntimeReminder(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)

	args := json.RawMessage(`{"file_path":"main.go"}`)
	for i := 0; i < repeatedToolCallThreshold; i++ {
		toolID := "read-" + string(rune('a'+i))
		s.handleAgentEvent(agentcore.Event{Type: agentcore.EventToolExecStart, ToolID: toolID, Tool: "read", Args: args})
		s.handleAgentEvent(agentcore.Event{Type: agentcore.EventToolExecEnd, ToolID: toolID, Tool: "read"})
	}

	msg := s.buildUserMessage(agentcore.TextBlock("continue"))
	if len(msg.Content) == 0 || !strings.Contains(msg.Content[0].Text, "repeatedly calling the same tool") {
		t.Fatalf("expected repeated-call reminder, got %#v", msg.Content)
	}
}

func TestDeliverRuntimeReminderSteersCurrentRun(t *testing.T) {
	t.Parallel()

	model := &scriptedReminderModel{}
	readTool := &stubExecTool{name: "read"}
	ag := agentcore.NewAgent(agentcore.WithModel(model), agentcore.WithTools(readTool), agentcore.WithMaxTurns(10))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 10},
		Cwd:      t.TempDir(),
		Tools:    []agentcore.Tool{readTool},
	})
	t.Cleanup(s.Close)

	if err := s.Prompt("start"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		return model.SawRepeatedToolReminder() && s.LastAssistantText() == "steered"
	})
}

func TestContinueWithRuntimeReminderAutoContinuesWhenIdle(t *testing.T) {
	t.Parallel()

	model := &scriptedReminderModel{}
	ag := agentcore.NewAgent(agentcore.WithModel(model), agentcore.WithMaxTurns(10))
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "initial task"),
		textMessage(agentcore.RoleAssistant, "task completed."),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 10},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)

	s.continueWithRuntimeReminder("test_reminder:1:0", ReminderRepeatToolCall, "<system-reminder>\ntest runtime reminder.\n</system-reminder>")
	waitFor(t, time.Second, func() bool {
		return s.LastAssistantText() == "steered"
	})
}

func TestGoalContinuationAutoContinuesWhenIdle(t *testing.T) {
	t.Parallel()

	model := &countingGoalModel{}
	ag := agentcore.NewAgent(agentcore.WithModel(model), agentcore.WithMaxTurns(10))
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "initial task"),
		textMessage(agentcore.RoleAssistant, "partial progress."),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 10},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)
	var signalCalls atomic.Int32
	s.SetGoalSignal(func() goalstate.Signal {
		if signalCalls.Add(1) > 1 {
			return goalstate.Signal{}
		}
		return goalstate.Signal{
			Active:   true,
			Key:      "goal:test",
			Reminder: "<system-reminder>\nContinue the explicit goal.\n</system-reminder>",
		}
	})

	s.runtime.afterAgentEnd()
	waitFor(t, time.Second, func() bool {
		return s.LastAssistantText() == "goal run 1"
	})
}

func TestSessionEmitsGoalEvents(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 10},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)

	var events []SessionEvent
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type == SEGoalUpdated || ev.Type == SEGoalCleared {
			events = append(events, ev)
		}
	})
	defer unsub()

	s.HandleGoalChange(goalstate.Change{
		Previous: goalstate.State{Status: goalstate.StatusOff},
		Current: goalstate.State{
			ID:        "goal-1",
			Objective: "ship goal mode",
			Status:    goalstate.StatusActive,
		},
	})
	s.HandleGoalChange(goalstate.Change{
		Previous: goalstate.State{
			ID:        "goal-1",
			Objective: "ship goal mode",
			Status:    goalstate.StatusActive,
		},
		Current: goalstate.State{Status: goalstate.StatusOff},
	})

	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].Type != SEGoalUpdated || events[0].Goal.Status != goalstate.StatusActive {
		t.Fatalf("first event = (%s, %s), want goal_updated active", events[0].Type, events[0].Goal.Status)
	}
	if events[1].Type != SEGoalCleared || events[1].GoalPrevious.Status != goalstate.StatusActive {
		t.Fatalf("second event = (%s, prev %s), want goal_cleared prev active", events[1].Type, events[1].GoalPrevious.Status)
	}
}

func TestSessionUsageLimitErrorMarksGoal(t *testing.T) {
	t.Parallel()

	s := newEventTestSession(t)
	var markedReason string
	s.SetGoalUsageLimitHandler(func(reason string) (goalstate.State, error) {
		markedReason = reason
		return goalstate.State{Status: goalstate.StatusUsageLimited}, nil
	})

	var sawAgentError bool
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type == SEAgentEvent && ev.AgentEvent != nil && ev.AgentEvent.Type == agentcore.EventError {
			sawAgentError = true
		}
	})
	defer unsub()

	s.handleAgentEvent(agentcore.Event{
		Type: agentcore.EventError,
		Err:  fmt.Errorf("usage_limit_reached: The usage limit has been reached"),
	})

	if markedReason != "provider usage limit reached" {
		t.Fatalf("marked reason = %q", markedReason)
	}
	if !sawAgentError {
		t.Fatal("expected original agent error event to still be emitted")
	}
}

func TestSessionPlainRateLimitErrorDoesNotMarkGoalUsageLimited(t *testing.T) {
	t.Parallel()

	s := newEventTestSession(t)
	marked := false
	s.SetGoalUsageLimitHandler(func(reason string) (goalstate.State, error) {
		marked = true
		return goalstate.State{Status: goalstate.StatusUsageLimited}, nil
	})

	s.handleAgentEvent(agentcore.Event{
		Type: agentcore.EventError,
		Err:  fmt.Errorf("provider returned 429 rate limit"),
	})

	if marked {
		t.Fatal("did not expect plain rate limit error to mark goal usage-limited")
	}
}

func TestSessionNonUsageLimitErrorDoesNotMarkGoal(t *testing.T) {
	t.Parallel()

	s := newEventTestSession(t)
	marked := false
	s.SetGoalUsageLimitHandler(func(reason string) (goalstate.State, error) {
		marked = true
		return goalstate.State{Status: goalstate.StatusUsageLimited}, nil
	})

	s.handleAgentEvent(agentcore.Event{
		Type: agentcore.EventError,
		Err:  fmt.Errorf("plain model failure"),
	})

	if marked {
		t.Fatal("did not expect non-rate-limit error to mark goal usage-limited")
	}
}

func TestSessionUsageLimitMarkFailureEmitsExplicitError(t *testing.T) {
	t.Parallel()

	s := newEventTestSession(t)
	s.SetGoalUsageLimitHandler(func(reason string) (goalstate.State, error) {
		return goalstate.State{}, fmt.Errorf("no active goal")
	})

	var sawSessionError bool
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type == SEError && ev.Error != nil && strings.Contains(ev.Error.Error(), "mark goal usage-limited") {
			sawSessionError = true
		}
	})
	defer unsub()

	s.handleAgentEvent(agentcore.Event{
		Type: agentcore.EventError,
		Err:  fmt.Errorf("usage_limit_reached: The usage limit has been reached"),
	})

	if !sawSessionError {
		t.Fatal("expected usage-limit mark failure to emit explicit session error")
	}
}

func TestGoalContinuationCanAutoContinueAcrossRuns(t *testing.T) {
	t.Parallel()

	model := &countingGoalModel{}
	ag := agentcore.NewAgent(agentcore.WithModel(model), agentcore.WithMaxTurns(10))
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "initial task"),
		textMessage(agentcore.RoleAssistant, "partial progress."),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 10},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)
	var signalCalls atomic.Int32
	s.SetGoalSignal(func() goalstate.Signal {
		if signalCalls.Add(1) > 2 {
			return goalstate.Signal{}
		}
		return goalstate.Signal{
			Active:   true,
			Key:      "goal:test",
			Reminder: "<system-reminder>\nContinue the explicit goal.\n</system-reminder>",
		}
	})

	s.runtime.afterAgentEnd()
	waitFor(t, time.Second, func() bool {
		return model.ReminderRuns() == 2 && s.LastAssistantText() == "goal run 2"
	})
}

func TestGoalContinuationDoesNotBypassQueuedRuntimeReminder(t *testing.T) {
	t.Parallel()

	model := &scriptedReminderModel{}
	ag := agentcore.NewAgent(agentcore.WithModel(model), agentcore.WithMaxTurns(10))
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "initial task"),
		textMessage(agentcore.RoleAssistant, "partial progress."),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 10},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)
	s.SetGoalSignal(func() goalstate.Signal {
		return goalstate.Signal{
			Active:   true,
			Key:      "goal:test",
			Reminder: "<system-reminder>\nContinue the explicit goal.\n</system-reminder>",
		}
	})
	s.queueRuntimeReminder("queued", ReminderTaskManagement, "<system-reminder>\nqueued reminder\n</system-reminder>")

	s.runtime.afterAgentEnd()
	time.Sleep(50 * time.Millisecond)
	if got := s.LastAssistantText(); got != "partial progress." {
		t.Fatalf("goal continuation bypassed queued reminder, last assistant = %q", got)
	}
}

func TestPromptDoesNotQueueTaskManagementReminderFromUserText(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:     ag,
		Settings:  config.Resolved{MaxTurns: 30},
		Cwd:       t.TempDir(),
		TaskStore: storage.NewTaskStore(),
	})
	t.Cleanup(s.Close)

	s.beginTurn()
	s.runtime.beforeUserPrompt([]agentcore.ContentBlock{
		agentcore.TextBlock("Build a complete project: a Go CLI app that lets AI agents autonomously write novels."),
	})

	msg := s.buildUserMessage(agentcore.TextBlock("start"))
	if len(msg.Content) != 1 {
		t.Fatalf("expected no pre-prompt task reminder blocks, got %#v", msg.Content)
	}
	if msg.Content[0].Text != "start" {
		t.Fatalf("unexpected injected prompt content: %#v", msg.Content)
	}
}

func TestTaskManagementReminderSteersBeforeStopWithOpenInProgressTask(t *testing.T) {
	t.Parallel()

	store := storage.NewTaskStore()
	task := store.Create("Summarize project state", "Write the final analysis summary", "Summarizing project state", nil)
	inProgress := storage.TaskInProgress
	if _, err := store.Update(task.ID, storage.TaskUpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("set task in_progress: %v", err)
	}

	model := &taskCompletionReminderModel{taskID: task.ID}
	taskTools := localtools.NewTaskTools(store, nil, nil)
	ag := agentcore.NewAgent(
		agentcore.WithModel(model),
		agentcore.WithTools(taskTools...),
		agentcore.WithMaxTurns(10),
	)
	s := NewSession(SessionConfig{
		Agent:     ag,
		Settings:  config.Resolved{MaxTurns: 10},
		Cwd:       t.TempDir(),
		TaskStore: store,
		Tools:     taskTools,
	})
	t.Cleanup(s.Close)

	if err := s.Prompt("分析一下项目"); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		snap := store.Snapshot()
		return model.SawReminder() && snap.Completed == 1 && snap.InProgress == 0 && s.LastAssistantText() == "已补上任务完成状态"
	})
}

func TestRuntimeMetricsTrackCompactionSavings(t *testing.T) {
	t.Parallel()

	model := &stubChatModel{}
	manager := agentctx.NewEngine(agentctx.EngineConfig{
		ContextWindow: 16,
		Strategies: []agentctx.Strategy{
			agentctx.NewFullSummary(agentctx.FullSummaryConfig{Model: model, KeepRecentTokens: 1}),
		},
	})
	ag := agentcore.NewAgent(agentcore.WithModel(model))
	s := NewSession(SessionConfig{
		Agent:          ag,
		ContextManager: manager,
		Settings:       config.Resolved{MaxTurns: 30},
		Cwd:            t.TempDir(),
	})
	t.Cleanup(s.Close)

	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, strings.Repeat("X", 300)),
		textMessage(agentcore.RoleAssistant, "recent"),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	if _, err := s.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}

	metrics := s.RuntimeMetrics()
	if metrics.CompactionTotal != 1 || metrics.CompactionChanged != 1 || metrics.CompactionSaved <= 0 {
		t.Fatalf("unexpected compaction metrics: %#v", metrics)
	}
}

func TestHandleProjectedRewriteUpdatesMetrics(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)

	s.HandleProjectedRewrite(agentctx.RewriteEvent{
		Reason:       "threshold",
		Strategy:     "light_trim",
		Changed:      true,
		TokensBefore: 1000,
		TokensAfter:  600,
	})

	metrics := s.RuntimeMetrics()
	if metrics.CompactionByKind[CompactionKindTrim] != 1 || metrics.CompactionSavedByKind[CompactionKindTrim] != 400 {
		t.Fatalf("unexpected trim compaction metrics: %#v", metrics)
	}
}

// Regression: ephemeralQuery must forward the parent agent's tool specs so
// Anthropic's prompt cache hits the system-block prefix. The wire order is
// `tools → system → messages`, and we set the cache breakpoint on the last
// system block — sending `tools: nil` (the previous behaviour) made the
// prefix byte-different from the main loop and forfeited a ~10K-token cache
// read on every /btw and post-turn suggestion call.
func TestEphemeralQueryForwardsToolsForCacheHit(t *testing.T) {
	t.Parallel()

	model := &captureChatModel{}
	noopTool := agentcore.NewFuncTool("noop", "does nothing",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return json.Marshal("ok")
		})
	ag := agentcore.NewAgent(
		agentcore.WithModel(model),
		agentcore.WithTools(noopTool),
	)
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "hi"),
		textMessage(agentcore.RoleAssistant, "hello"),
	}); err != nil {
		t.Fatalf("seed messages: %v", err)
	}

	s := NewSession(SessionConfig{
		Agent:     ag,
		ChatModel: model,
		Settings:  config.Resolved{MaxTurns: 10},
		Cwd:       t.TempDir(),
	})
	t.Cleanup(s.Close)

	if _, err := s.SideQuestion(context.Background(), "follow-up?"); err != nil {
		t.Fatalf("SideQuestion: %v", err)
	}

	tools := model.CapturedTools()
	if len(tools) != 1 {
		t.Fatalf("CapturedTools length = %d, want 1 (the parent's noop tool)", len(tools))
	}
	if tools[0].Name != "noop" {
		t.Errorf("CapturedTools[0].Name = %q, want %q", tools[0].Name, "noop")
	}
}

// Regression: a thinking + tool_use assistant must not be forwarded as a
// thinking-only block, which the litellm bridge would serialize as empty and
// OpenAI would reject.
func TestEphemeralQueryDropsThinkingOnlyAssistant(t *testing.T) {
	t.Parallel()

	model := &captureChatModel{}
	ag := agentcore.NewAgent(agentcore.WithModel(model))

	thinkingOnlyAssistant := agentcore.Message{
		Role: agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{
			agentcore.ThinkingBlock("planning the search"),
			agentcore.ToolCallBlock(agentcore.ToolCall{ID: "t1", Name: "ls", Args: json.RawMessage(`{}`)}),
		},
		StopReason: agentcore.StopReasonToolUse,
	}
	toolResult := agentcore.Message{
		Role:     agentcore.RoleTool,
		Content:  []agentcore.ContentBlock{agentcore.TextBlock("result")},
		Metadata: map[string]any{"tool_call_id": "t1"},
	}
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "explore"),
		thinkingOnlyAssistant,
		toolResult,
		textMessage(agentcore.RoleAssistant, "found it"),
	}); err != nil {
		t.Fatalf("seed messages: %v", err)
	}

	s := NewSession(SessionConfig{
		Agent:     ag,
		ChatModel: model,
		Settings:  config.Resolved{MaxTurns: 10},
		Cwd:       t.TempDir(),
	})
	t.Cleanup(s.Close)

	if _, err := s.SideQuestion(context.Background(), "side?"); err != nil {
		t.Fatalf("SideQuestion: %v", err)
	}

	captured := model.Captured()
	if len(captured) == 0 {
		t.Fatalf("expected captured messages, got none")
	}

	for i, msg := range captured {
		if msg.Role != agentcore.RoleAssistant {
			continue
		}
		var hasText bool
		for _, b := range msg.Content {
			if b.Type == agentcore.ContentText && strings.TrimSpace(b.Text) != "" {
				hasText = true
				break
			}
		}
		if !hasText {
			t.Fatalf("captured[%d] assistant has no non-empty text block: %#v", i, msg.Content)
		}
	}
}
