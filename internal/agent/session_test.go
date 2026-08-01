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

type toolCallThenStopModel struct {
	calls atomic.Int32
}

func (m *toolCallThenStopModel) Generate(
	context.Context,
	[]agentcore.Message,
	[]agentcore.ToolSpec,
	...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	return nil, errors.New("Generate not used")
}

func (m *toolCallThenStopModel) GenerateStream(
	context.Context,
	[]agentcore.Message,
	[]agentcore.ToolSpec,
	...agentcore.CallOption,
) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent, 3)
	if m.calls.Add(1) == 1 {
		call := agentcore.ToolCall{
			ID:   "persist-before-execute",
			Name: "inspect_persistence",
			Args: json.RawMessage(`{}`),
		}
		msg := agentcore.Message{
			Role:       agentcore.RoleAssistant,
			Content:    []agentcore.ContentBlock{agentcore.ToolCallBlock(call)},
			StopReason: agentcore.StopReasonToolUse,
		}
		ch <- agentcore.StreamEvent{Type: agentcore.StreamEventToolCallStart, Message: msg}
		ch <- agentcore.StreamEvent{Type: agentcore.StreamEventToolCallEnd, Message: msg, CompletedToolCall: &call}
		ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: msg, StopReason: agentcore.StopReasonToolUse}
	} else {
		msg := agentcore.Message{
			Role:       agentcore.RoleAssistant,
			Content:    []agentcore.ContentBlock{agentcore.TextBlock("done")},
			StopReason: agentcore.StopReasonStop,
		}
		ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: msg, StopReason: agentcore.StopReasonStop}
	}
	close(ch)
	return ch, nil
}

func (m *toolCallThenStopModel) SupportsTools() bool { return true }

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

type blockingChatModel struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func newBlockingChatModel() *blockingChatModel {
	return &blockingChatModel{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (m *blockingChatModel) Generate(
	ctx context.Context,
	_ []agentcore.Message,
	_ []agentcore.ToolSpec,
	_ ...agentcore.CallOption,
) (*agentcore.LLMResponse, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		close(m.started)
		select {
		case <-m.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &agentcore.LLMResponse{Message: assistantTextMessage(fmt.Sprintf("call %d", call))}, nil
}

func (m *blockingChatModel) GenerateStream(
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

func (m *blockingChatModel) SupportsTools() bool { return true }

func (m *blockingChatModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
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

	oldPath := s.persist.currentStore().Path()
	oldProvider := s.Provider()
	oldModel := s.ModelName()

	err = s.SwitchSession(target.Header().SessionID)
	if err == nil {
		t.Fatalf("expected switch failure")
	}
	if !strings.Contains(err.Error(), "restore model") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := s.persist.currentStore().Path(); got != oldPath {
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
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads USERPROFILE on Windows

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
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads USERPROFILE on Windows

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
				"openai-proxy": {
					Type:   "openai",
					API:    "responses",
					APIKey: "openai-proxy-key",
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

	if err := s.SetModel("openai-proxy", "gpt-5.4"); err != nil {
		t.Fatalf("SetModel openai-proxy: %v", err)
	}
	if gotExtra["api"] != "responses" {
		t.Fatalf("api extra = %#v, want responses", gotExtra["api"])
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
	if tools := s.prompt.activeToolsSnapshot(); len(tools) != 1 || tools[0].Name() != "read" {
		t.Fatalf("expected filtered active tools to remain unchanged, got %#v", s.prompt.activeToolsSnapshot())
	}
}

func TestBuildUserMessagePrependsRuntimeRemindersBeforeUserText(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)

	s.queueRuntimeReminder("loop", ReminderRepeatToolCall, "<system-reminder>\nruntime reminder\n</system-reminder>")
	msg := s.buildUserMessage(agentcore.TextBlock("user input"))
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msg.Content))
	}
	if !strings.Contains(msg.Content[0].Text, "runtime reminder") {
		t.Fatalf("runtime reminder should come first: %#v", msg.Content)
	}
	if msg.Content[1].Text != "user input" {
		t.Fatalf("user text must be the last block: %#v", msg.Content)
	}
}

// The reason system block 2 exists: nothing that is stable for the session may
// ride along with user messages, or the history grows one copy per turn.
// Before this layout, five turns put five copies of AGENTS.md / MEMORY.md /
// the skill listing into the request.
func TestUserMessagesDoNotAccumulateWorkspaceContext(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      t.TempDir(),
		ContextFiles: config.ContextFiles{
			Agents:    "AGENTS-MARKER",
			Memory:    "MEMORY-MARKER",
			MemoryDir: t.TempDir(),
		},
		Skills: []skill.Spec{{Name: "probe", Description: "SKILL-MARKER", FilePath: "/s/p.md"}},
	})
	t.Cleanup(s.Close)

	const turns = 5
	for range turns {
		if err := s.Prompt("hello"); err != nil {
			t.Fatal(err)
		}
		s.WaitForIdle()
	}

	for _, marker := range []string{"AGENTS-MARKER", "MEMORY-MARKER", "SKILL-MARKER"} {
		if n := countInMessages(s.deps.agent.Messages(), marker); n != 0 {
			t.Errorf("%s appears %d times in message history; it belongs in system block 2 only", marker, n)
		}
	}
}

func countInMessages(msgs []agentcore.AgentMessage, marker string) int {
	n := 0
	for _, am := range msgs {
		msg, ok := am.(agentcore.Message)
		if !ok {
			continue
		}
		for _, b := range msg.Content {
			if b.Type == agentcore.ContentText {
				n += strings.Count(b.Text, marker)
			}
		}
	}
	return n
}

// The date lives in system block 1, so a same-day session must never spend a
// reminder on it; only a rollover gets one, and only once.
func TestDateChangeReminderFiresOnlyOnRollover(t *testing.T) {
	t.Parallel()

	// Baseline = the date system block 1 was rendered with.
	r := reminderState{lastDate: "2026-07-31"}
	if r.takeDateChange("2026-07-31") {
		t.Fatal("same date must not fire")
	}
	if !r.takeDateChange("2026-08-01") {
		t.Fatal("rollover must fire")
	}
	if r.takeDateChange("2026-08-01") {
		t.Fatal("rollover must fire exactly once")
	}
}

// BuildUniversalBase must stay a pure function of its inputs: the leader
// renders block 1 at boot, a worktree teammate re-renders it at spawn, and a
// live clock in there would split their shared cache prefix after midnight.
func TestUniversalBaseIsDeterministic(t *testing.T) {
	t.Parallel()

	first, second := config.BuildUniversalBase("/tmp/ws"), config.BuildUniversalBase("/tmp/ws")
	if first != second {
		t.Fatal("universal base is not deterministic — leader and teammate will not share a cache prefix")
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

func TestSessionPersistsAssistantToolCallBeforeExecution(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewManager(dir).Create(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	checked := make(chan error, 1)
	tool := agentcore.NewFuncTool(
		"inspect_persistence",
		"verify the tool call is durable before execution",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, json.RawMessage) (json.RawMessage, error) {
			snapshot, err := store.BuildSnapshot()
			if err != nil {
				checked <- fmt.Errorf("build snapshot during tool execution: %w", err)
				return nil, err
			}
			for _, message := range snapshot.Messages {
				concrete, ok := message.(agentcore.Message)
				if !ok || concrete.Role != agentcore.RoleAssistant {
					continue
				}
				for _, call := range concrete.ToolCalls() {
					if call.ID == "persist-before-execute" {
						checked <- nil
						return json.RawMessage(`"ok"`), nil
					}
				}
			}
			err = errors.New("assistant tool-call message was not persisted before execution")
			checked <- err
			return nil, err
		},
	)
	model := &toolCallThenStopModel{}
	ag := agentcore.NewAgent(
		agentcore.WithModel(model),
		agentcore.WithTools(tool),
		agentcore.WithMaxTurns(3),
	)
	s := NewSession(SessionConfig{
		Agent:    ag,
		Store:    store,
		Settings: config.Resolved{MaxTurns: 3},
		Cwd:      dir,
		Tools:    []agentcore.Tool{tool},
	})
	t.Cleanup(s.Close)

	if err := s.Prompt("run tool"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	ag.WaitForIdle()

	select {
	case err := <-checked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool did not verify persisted assistant call")
	}
}

func TestSessionPersistenceFailurePreventsToolExecution(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewManager(dir).Create(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	var executeCalls atomic.Int64
	tool := agentcore.NewFuncTool(
		"inspect_persistence",
		"must not execute after persistence failure",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, json.RawMessage) (json.RawMessage, error) {
			executeCalls.Add(1)
			return json.RawMessage(`"unexpected"`), nil
		},
	)
	model := &toolCallThenStopModel{}
	ag := agentcore.NewAgent(
		agentcore.WithModel(model),
		agentcore.WithTools(tool),
		agentcore.WithMaxTurns(3),
	)
	s := NewSession(SessionConfig{
		Agent:       ag,
		Store:       store,
		LazyPersist: true,
		Settings:    config.Resolved{MaxTurns: 3},
		Cwd:         dir,
		Tools:       []agentcore.Tool{tool},
	})
	t.Cleanup(s.Close)

	if err := s.Prompt("run tool"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	ag.WaitForIdle()

	if executeCalls.Load() != 0 {
		t.Fatalf("tool execute calls = %d, want 0", executeCalls.Load())
	}
	if state := ag.State(); !strings.Contains(state.Error, "persist message") {
		t.Fatalf("agent error = %q, want persistence failure", state.Error)
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

func TestBackgroundResultAutoContinuesWhenIdle(t *testing.T) {
	t.Parallel()

	model := &countingChatModel{}
	ag := agentcore.NewAgent(agentcore.WithModel(model))
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "initial task"),
		textMessage(agentcore.RoleAssistant, "task completed"),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	s := NewSession(SessionConfig{Agent: ag, Cwd: t.TempDir()})
	t.Cleanup(s.Close)

	s.EnqueueBackgroundResult(agentcore.UserMsg("background result"))
	waitFor(t, time.Second, func() bool {
		return model.Calls() == 1 && !ag.HasFollowUps()
	})

	messages := ag.Messages()
	if len(messages) < 2 || messages[len(messages)-2].TextContent() != "background result" {
		t.Fatalf("background result was not delivered before the response: %#v", messages)
	}
}

func TestBackgroundResultWaitsForRunningTurn(t *testing.T) {
	t.Parallel()

	model := newBlockingChatModel()
	ag := agentcore.NewAgent(agentcore.WithModel(model))
	s := NewSession(SessionConfig{Agent: ag, Cwd: t.TempDir()})
	t.Cleanup(s.Close)

	if err := s.Prompt("start"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	select {
	case <-model.started:
	case <-time.After(time.Second):
		t.Fatal("first model call did not start")
	}

	s.EnqueueBackgroundResult(agentcore.UserMsg("background result"))
	if got := model.Calls(); got != 1 {
		t.Fatalf("background result interrupted the active call: calls = %d", got)
	}
	close(model.release)

	waitFor(t, time.Second, func() bool {
		return model.Calls() == 2 && !ag.HasFollowUps()
	})
	messages := ag.Messages()
	if len(messages) < 2 || messages[len(messages)-2].TextContent() != "background result" {
		t.Fatalf("background result was not consumed after the active turn: %#v", messages)
	}
}

func TestConcurrentBackgroundResultsAreAllConsumed(t *testing.T) {
	t.Parallel()

	model := &countingChatModel{}
	ag := agentcore.NewAgent(agentcore.WithModel(model))
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "initial task"),
		textMessage(agentcore.RoleAssistant, "task completed"),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	s := NewSession(SessionConfig{Agent: ag, Cwd: t.TempDir()})
	t.Cleanup(s.Close)

	var (
		wg       sync.WaitGroup
		errorMu  sync.Mutex
		runError error
	)
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type != SEError {
			return
		}
		errorMu.Lock()
		runError = ev.Error
		errorMu.Unlock()
	})
	defer unsub()

	const resultCount = 8
	for i := 0; i < resultCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.EnqueueBackgroundResult(agentcore.UserMsg(fmt.Sprintf("background result %d", i)))
		}(i)
	}
	wg.Wait()
	waitFor(t, time.Second, func() bool {
		return !ag.State().IsRunning && !ag.HasFollowUps()
	})

	errorMu.Lock()
	err := runError
	errorMu.Unlock()
	if err != nil {
		t.Fatalf("concurrent background results caused an error: %v", err)
	}

	seen := make(map[string]bool, resultCount)
	for _, message := range ag.Messages() {
		seen[message.TextContent()] = true
	}
	for i := 0; i < resultCount; i++ {
		text := fmt.Sprintf("background result %d", i)
		if !seen[text] {
			t.Fatalf("missing %q from conversation", text)
		}
	}
}

func TestAbortKeepsBackgroundResultQueuedWithoutRestart(t *testing.T) {
	t.Parallel()

	model := newBlockingChatModel()
	ag := agentcore.NewAgent(agentcore.WithModel(model))
	s := NewSession(SessionConfig{Agent: ag, Cwd: t.TempDir()})
	t.Cleanup(s.Close)

	if err := s.Prompt("start"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	select {
	case <-model.started:
	case <-time.After(time.Second):
		t.Fatal("first model call did not start")
	}
	s.Abort()
	ag.WaitForIdle()
	s.EnqueueBackgroundResult(agentcore.UserMsg("background result"))
	time.Sleep(50 * time.Millisecond)
	if got := model.Calls(); got != 1 {
		t.Fatalf("background result restarted an aborted session: calls = %d", got)
	}
	if !ag.HasFollowUps() {
		t.Fatal("background result was lost after abort")
	}

	if err := s.Prompt("next request"); err != nil {
		t.Fatalf("prompt after abort: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		return model.Calls() == 3 && !ag.HasFollowUps()
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

// The engine now commits a summary rewrite on its own, so the session file has
// to record the checkpoint too. Without it resume replays the history the
// summary retired and pays to summarize it again — and the runtime baseline and
// the store disagree in the meantime.
func TestCommittedRewriteRecordsCheckpoint(t *testing.T) {
	t.Parallel()

	newSessionWithStore := func(t *testing.T) (*Session, *storage.Store) {
		t.Helper()
		dir := t.TempDir()
		store, err := storage.NewManager(dir).Create(dir)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		for range 3 {
			if err := store.AppendMessage(agentcore.UserMsg("pre-compaction noise")); err != nil {
				t.Fatalf("append: %v", err)
			}
		}
		s := NewSession(SessionConfig{
			Agent:    agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
			Store:    store,
			Settings: config.Resolved{MaxTurns: 30},
			Cwd:      dir,
		})
		t.Cleanup(s.Close)
		return s, store
	}

	summaryView := []agentcore.AgentMessage{
		agentctx.ContextSummary{Summary: "what came before"},
		agentcore.UserMsg("kept tail"),
	}

	t.Run("summary commit", func(t *testing.T) {
		s, store := newSessionWithStore(t)
		s.HandleProjectedRewrite(agentctx.RewriteEvent{
			Reason:    "threshold",
			Strategy:  "full_summary",
			Changed:   true,
			Committed: true,
			Info:      &agentctx.SummaryInfo{CompactedCount: 6, KeptCount: 1},
			View:      summaryView,
		})

		snap, err := store.BuildSnapshot()
		if err != nil {
			t.Fatalf("build snapshot: %v", err)
		}
		if len(snap.Messages) != 2 {
			t.Fatalf("snapshot has %d messages, want summary + tail", len(snap.Messages))
		}
		if got := snap.Messages[0].TextContent(); !strings.Contains(got, "what came before") {
			t.Fatalf("messages[0] = %q, want the summary", got)
		}
	})

	// Clearing tool results produces no checkpoint, so there is nothing for
	// resume to replay from — writing one would truncate the history to a
	// summary that does not exist.
	t.Run("tool result commit writes nothing", func(t *testing.T) {
		s, store := newSessionWithStore(t)
		s.HandleProjectedRewrite(agentctx.RewriteEvent{
			Reason:    "threshold",
			Strategy:  "tool_result_microcompact",
			Changed:   true,
			Committed: true,
			View:      []agentcore.AgentMessage{agentcore.UserMsg("cleared")},
		})

		snap, err := store.BuildSnapshot()
		if err != nil {
			t.Fatalf("build snapshot: %v", err)
		}
		if len(snap.Messages) != 3 {
			t.Fatalf("snapshot has %d messages, want the 3 originals untouched", len(snap.Messages))
		}
	})
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

func TestResetDuringActiveRunLeavesSessionUsable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := storage.NewManager(dir)
	store, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	model := newBlockingChatModel()
	ag := agentcore.NewAgent(agentcore.WithModel(model))
	s := NewSession(SessionConfig{Agent: ag, Store: store, Manager: mgr, Cwd: dir})
	t.Cleanup(s.Close)

	var mu sync.Mutex
	var sessionErrs []error
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type == SEError {
			mu.Lock()
			sessionErrs = append(sessionErrs, ev.Error)
			mu.Unlock()
		}
	})
	defer unsub()

	if err := s.Prompt("start"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	select {
	case <-model.started:
	case <-time.After(time.Second):
		t.Fatal("model call did not start")
	}

	if err := s.Reset(); err != nil {
		t.Fatalf("reset during active run: %v", err)
	}
	if s.IsRunning() {
		t.Fatal("still running after reset")
	}
	if got := len(ag.Messages()); got != 0 {
		t.Fatalf("history not cleared after reset: %d messages", got)
	}

	if err := s.Prompt("fresh start"); err != nil {
		t.Fatalf("prompt after reset: %v", err)
	}
	waitFor(t, time.Second, func() bool { return !s.IsRunning() })

	mu.Lock()
	defer mu.Unlock()
	if len(sessionErrs) > 0 {
		t.Fatalf("unexpected SEError during reset-while-running: %v", sessionErrs)
	}
}

func TestContinuationDuringHoldIsSilentlyDropped(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "task"),
		textMessage(agentcore.RoleAssistant, "partial answer."),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	s := NewSession(SessionConfig{Agent: ag, Cwd: t.TempDir()})
	t.Cleanup(s.Close)

	var mu sync.Mutex
	var sessionErrs []error
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type == SEError {
			mu.Lock()
			sessionErrs = append(sessionErrs, ev.Error)
			mu.Unlock()
		}
	})
	defer unsub()

	release := ag.HoldRuns()
	defer release()

	s.EnqueueBackgroundResult(agentcore.UserMsg("background result"))
	if s.IsRunning() {
		t.Fatal("continuation started despite held run lifecycle")
	}
	if !ag.HasFollowUps() {
		t.Fatal("background result must stay queued while held")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sessionErrs) > 0 {
		t.Fatalf("held continuation must be silent, got %v", sessionErrs)
	}
}

func TestAutoResumeReminderFallsBackToQueueWhileHeld(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "task"),
		textMessage(agentcore.RoleAssistant, "partial answer."),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	s := NewSession(SessionConfig{Agent: ag, Cwd: t.TempDir()})
	t.Cleanup(s.Close)

	release := ag.HoldRuns()
	defer release()

	const reminder = "<system-reminder>\nheld reminder\n</system-reminder>"
	s.continueWithRuntimeReminder("held_key", ReminderRepeatToolCall, reminder)

	if s.IsRunning() {
		t.Fatal("auto-resume started despite held run lifecycle")
	}
	// Fast-fail must leave nothing in the agent's steering queue — a queued
	// copy plus the next-prompt fallback would deliver the reminder twice.
	if ag.HasQueuedMessages() {
		t.Fatal("held inject leaked into the steering queue")
	}
	runtime := s.reminders.drainForPrompt()
	count := 0
	for _, r := range runtime {
		if r == reminder {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("reminder queued %d times, want exactly 1 (next-prompt fallback)", count)
	}
}

// --- workspace retarget & self-healing preamble -----------------------------

func newPromptTestSession(t *testing.T, cwd string) *Session {
	t.Helper()
	s := NewSession(SessionConfig{
		Agent:                 agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Settings:              config.Resolved{MaxTurns: 10},
		Cwd:                   cwd,
		FrozenIdentity:        config.BuildUniversalBase(cwd),
		DeferredToolsPreamble: "<available-deferred-tools>\nWebSearch\n</available-deferred-tools>",
	})
	t.Cleanup(s.Close)
	return s
}

func TestRetargetWorkspaceRebuildsIdentityBlock(t *testing.T) {
	t.Parallel()

	origin, moved := t.TempDir(), t.TempDir()
	s := newPromptTestSession(t, origin)

	if got := s.prompt.frozenIdentity; !strings.Contains(got, origin) {
		t.Fatalf("block 1 does not name the starting cwd %q:\n%s", origin, got)
	}

	s.RetargetWorkspace(moved)

	identity := s.prompt.frozenIdentity
	if !strings.Contains(identity, moved) {
		t.Fatalf("block 1 still does not name the new cwd %q:\n%s", moved, identity)
	}
	if strings.Contains(identity, origin) {
		t.Fatalf("block 1 kept the stale cwd %q:\n%s", origin, identity)
	}
	if s.currentCwd() != moved {
		t.Fatalf("session cwd = %q, want %q", s.currentCwd(), moved)
	}
}

func TestRetargetWorkspaceRebuildsInstructionsFromNewRoot(t *testing.T) {
	t.Parallel()

	origin, moved := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(moved, "AGENTS.md"), []byte("worktree-only-marker"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	s := newPromptTestSession(t, origin)
	if strings.Contains(s.prompt.frozenInstructions, "worktree-only-marker") {
		t.Fatal("block 2 already carries the target workspace's AGENTS.md before retarget")
	}

	s.RetargetWorkspace(moved)

	if !strings.Contains(s.prompt.frozenInstructions, "worktree-only-marker") {
		t.Fatalf("block 2 missing the new workspace's AGENTS.md:\n%s", s.prompt.frozenInstructions)
	}
}

func TestRetargetWorkspaceHonorsSystemOverride(t *testing.T) {
	t.Parallel()

	origin, moved := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(moved, "SYSTEM.md"), []byte("override body"), 0o644); err != nil {
		t.Fatalf("write SYSTEM.md: %v", err)
	}

	s := newPromptTestSession(t, origin)
	s.RetargetWorkspace(moved)

	// An override replaces the whole prompt: block 1 must go away, not be
	// rebuilt alongside it.
	if s.prompt.frozenIdentity != "" {
		t.Fatalf("block 1 survived a SYSTEM.md override:\n%s", s.prompt.frozenIdentity)
	}
	if s.prompt.frozenInstructions != "override body" {
		t.Fatalf("block 2 = %q, want the override verbatim", s.prompt.frozenInstructions)
	}
}

func TestPendingPreambleIsSuppressedOnceDelivered(t *testing.T) {
	t.Parallel()

	s := newPromptTestSession(t, t.TempDir())

	preamble, ok := s.pendingPreamble()
	if !ok {
		t.Fatal("preamble not pending on an empty conversation")
	}
	if err := s.deps.agent.SetMessages([]agentcore.AgentMessage{injectedUserMsg(preamble)}); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	if _, ok := s.pendingPreamble(); ok {
		t.Fatal("preamble re-injected while the transcript still carries it")
	}
}

// The point of deriving delivery from the transcript: every path that wipes
// the context window re-arms the preamble without a flag to remember.
func TestPendingPreambleSelfHealsAfterWipe(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		wipe func(*Session)
	}{
		{"ClearConversation", func(s *Session) { s.ClearConversation() }},
		{"compaction drops it", func(s *Session) {
			if err := s.deps.agent.SetMessages([]agentcore.AgentMessage{
				agentcore.UserMsg("summary of the earlier conversation"),
			}); err != nil {
				t.Fatalf("replace transcript: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newPromptTestSession(t, t.TempDir())

			preamble, _ := s.pendingPreamble()
			if err := s.deps.agent.SetMessages([]agentcore.AgentMessage{injectedUserMsg(preamble)}); err != nil {
				t.Fatalf("seed transcript: %v", err)
			}
			tc.wipe(s)

			if _, ok := s.pendingPreamble(); !ok {
				t.Fatal("preamble stayed suppressed after the context window was replaced")
			}
		})
	}
}

func TestClearConversationDropsContextWindowState(t *testing.T) {
	t.Parallel()

	s := newPromptTestSession(t, t.TempDir())
	s.queueRuntimeReminder("k", ReminderHookContext, "queued")
	s.reminders.mu.Lock()
	s.reminders.recallSurfaced = map[string]bool{"mem": true}
	s.reminders.recallBytes = 512
	s.reminders.mu.Unlock()

	s.ClearConversation()

	s.reminders.mu.Lock()
	defer s.reminders.mu.Unlock()
	if len(s.reminders.runtime) != 0 {
		t.Fatalf("runtime reminders survived /clear: %v", s.reminders.runtime)
	}
	if len(s.reminders.recallSurfaced) != 0 || s.reminders.recallBytes != 0 {
		t.Fatalf("recall dedup state survived /clear: %v / %d bytes",
			s.reminders.recallSurfaced, s.reminders.recallBytes)
	}
}

// --- codex review follow-ups ------------------------------------------------

func TestIdentityBlockFollowsWorkspaceRetarget(t *testing.T) {
	t.Parallel()

	origin, moved := t.TempDir(), t.TempDir()
	s := newPromptTestSession(t, origin)

	// This is what the teammate spawner reads, per spawn. A shared teammate
	// runs in the leader's cwd, so a stale block would misname its workspace
	// AND split the prefix the two are supposed to share.
	before := s.IdentitySystemBlock()
	if len(before) != 1 || !strings.Contains(before[0].Text, origin) {
		t.Fatalf("identity block does not state the starting cwd: %+v", before)
	}
	if before[0].CacheControl != "ephemeral" {
		t.Fatalf("identity block CacheControl = %q, want ephemeral", before[0].CacheControl)
	}

	s.RetargetWorkspace(moved)

	after := s.IdentitySystemBlock()
	if len(after) != 1 || !strings.Contains(after[0].Text, moved) {
		t.Fatalf("identity block did not follow the retarget: %+v", after)
	}
	if strings.Contains(after[0].Text, origin) {
		t.Fatal("identity block still leaks the pre-retarget cwd")
	}
}

// A context wipe destroys the rollover correction but leaves the stale date in
// block 1, so the correction has to be re-sent rather than stay marked as
// delivered.
func TestDateCorrectionSurvivesContextWipe(t *testing.T) {
	t.Parallel()

	tomorrow := "2999-01-02"
	for _, tc := range []struct {
		name string
		wipe func(*reminderState)
	}{
		{"resetAll", func(r *reminderState) { r.resetAll() }},
		{"compaction", func(r *reminderState) { r.resetSummarized() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &reminderState{lastDate: config.SessionDate()}

			if !r.takeDateChange(tomorrow) {
				t.Fatal("rollover must fire once")
			}
			if r.takeDateChange(tomorrow) {
				t.Fatal("rollover must not fire twice within one context window")
			}

			tc.wipe(r)

			if !r.takeDateChange(tomorrow) {
				t.Fatal("rollover must fire again: the wipe dropped the correction but block 1 is still stale")
			}
		})
	}
}
