package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/memory"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/hooks"
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
		if msg.Role == agentcore.RoleUser && strings.Contains(msg.TextContent(), "重复调用同一个工具") {
			m.secondCallSawReminder = true
		}
	}

	m.callCount++
	if m.callCount == 1 && !sawInjectedReminder {
		return &agentcore.LLMResponse{
			Message: toolCallMessage(
				agentcore.ToolCall{ID: "tc1", Name: "read", Args: json.RawMessage(`{"path":"main.go"}`)},
				agentcore.ToolCall{ID: "tc2", Name: "read", Args: json.RawMessage(`{"path":"main.go"}`)},
				agentcore.ToolCall{ID: "tc3", Name: "read", Args: json.RawMessage(`{"path":"main.go"}`)},
				agentcore.ToolCall{ID: "tc4", Name: "read", Args: json.RawMessage(`{"path":"main.go"}`)},
			),
		}, nil
	}

	return &agentcore.LLMResponse{
		Message: assistantTextMessage("steered"),
	}, nil
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

type stubExecTool struct {
	name string
}

func (t *stubExecTool) Name() string           { return t.name }
func (t *stubExecTool) Description() string    { return "stub exec tool" }
func (t *stubExecTool) Schema() map[string]any { return nil }
func (t *stubExecTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
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
			ContextWindow:  128000,
			AutoCompaction: false,
			MaxTurns:       30,
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
			Provider: "openai",
			Model:    "good-model",
			Providers: map[string]config.ProviderConfig{
				"openai": {APIKey: "k"},
			},
			ContextWindow:  128000,
			AutoCompaction: false,
			MaxTurns:       30,
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
	if events != 0 {
		t.Fatalf("expected no model-changed events on failure, got %d", events)
	}
}

func TestSessionAgentEndFiresNotificationHookWithoutUI(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := filepath.Join(dir, "notification.marker")
	runner := hooks.New(config.HooksConfig{
		"Notification": {
			{Type: "command", Command: "touch " + marker},
		},
	}, "sess-test", nil)
	if runner == nil {
		t.Fatal("expected hook runner")
	}

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:      ag,
		Settings:   config.Resolved{MaxTurns: 30},
		Cwd:        dir,
		HookRunner: runner,
	})
	t.Cleanup(s.Close)

	s.handleAgentEvent(agentcore.Event{Type: agentcore.EventAgentEnd})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected notification hook to fire on agent end without UI")
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
	return &agentcore.LLMResponse{
		Message: assistantTextMessage(m.name),
	}, nil
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
	return &agentcore.LLMResponse{
		Message: assistantTextMessage("counted"),
	}, nil
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

func TestApplySkillInvocationUsesTemporaryOverrides(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	baseModel := &namedChatModel{name: "base-model"}
	s := NewSession(SessionConfig{
		Agent: agentcore.NewAgent(agentcore.WithModel(baseModel)),
		Settings: config.Resolved{
			Provider:       "openai",
			Model:          "base-model",
			ThinkingLevel:  "low",
			ContextWindow:  128000,
			AutoCompaction: false,
			MaxTurns:       30,
			Providers: map[string]config.ProviderConfig{
				"openai": {APIKey: "k", Models: []string{"base-model", "skill-model"}},
			},
		},
		Cwd:       dir,
		ChatModel: baseModel,
		CreateModel: func(_ string, model string, _ string, _ string) (agentcore.ChatModel, error) {
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
			Hooks: skill.HooksConfig{
				"Notification": {
					{Type: "command", Command: "echo hi"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ApplySkillInvocation error: %v", err)
	}

	if got := s.ModelName(); got != "skill-model" {
		t.Fatalf("temporary model = %q, want skill-model", got)
	}
	if got := s.Settings().ThinkingLevel; got != "high" {
		t.Fatalf("temporary thinking = %q, want high", got)
	}
	if hooks := s.CurrentSkillHooks(); len(hooks["Notification"]) != 1 {
		t.Fatalf("expected temporary notification hook, got %#v", hooks)
	}
	s.mu.Lock()
	reminders := append([]string(nil), s.runtimeReminders...)
	s.mu.Unlock()
	if len(reminders) == 0 || !strings.Contains(reminders[0], "internal/skill/**") {
		t.Fatalf("expected path reminder, got %#v", reminders)
	}

	s.clearSkillDelta()

	if got := s.ModelName(); got != "base-model" {
		t.Fatalf("restored model = %q, want base-model", got)
	}
	if got := s.Settings().ThinkingLevel; got != "low" {
		t.Fatalf("restored thinking = %q, want low", got)
	}
	if hooks := s.CurrentSkillHooks(); len(hooks["Notification"]) != 1 {
		t.Fatalf("expected session-scoped hooks to remain registered, got %#v", hooks)
	}
}

func TestSkillNotificationHooksRemainActiveAcrossTurns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := filepath.Join(dir, "skill-notification.marker")
	runner := hooks.New(config.HooksConfig{
		"Notification": {
			{Type: "command", Command: "true"},
		},
	}, "sess-test", nil)
	if runner == nil {
		t.Fatal("expected hook runner")
	}

	s := NewSession(SessionConfig{
		Agent:      agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Settings:   config.Resolved{MaxTurns: 30},
		Cwd:        dir,
		HookRunner: runner,
	})
	t.Cleanup(s.Close)
	runner.SetDynamicProvider(s.CurrentSkillHooks)

	err := s.ApplySkillInvocation(&skill.InvocationResult{
		Spec:       skill.Spec{Name: "review"},
		Mode:       skill.ModeInline,
		PromptText: "review prompt",
		Delta: skill.Delta{
			Hooks: skill.HooksConfig{
				"Notification": {
					{Type: "command", Command: "touch " + marker},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ApplySkillInvocation error: %v", err)
	}

	s.handleAgentEvent(agentcore.Event{Type: agentcore.EventAgentEnd})

	waitFor(t, 2*time.Second, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})

	if err := os.Remove(marker); err != nil {
		t.Fatalf("remove marker: %v", err)
	}
	runner.RunNotification(context.Background(), "second turn")
	waitFor(t, 2*time.Second, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})

	if hooks := s.CurrentSkillHooks(); len(hooks["Notification"]) != 1 {
		t.Fatalf("expected skill hooks to persist after turn end, got %#v", hooks)
	}
}

func TestApplySkillInvocationForkOnlyRecords(t *testing.T) {
	t.Parallel()

	baseModel := &namedChatModel{name: "base-model"}
	s := NewSession(SessionConfig{
		Agent: agentcore.NewAgent(agentcore.WithModel(baseModel)),
		Settings: config.Resolved{
			Provider:       "openai",
			Model:          "base-model",
			ThinkingLevel:  "low",
			ContextWindow:  128000,
			AutoCompaction: false,
			MaxTurns:       30,
		},
		Cwd:       t.TempDir(),
		ChatModel: baseModel,
	})
	t.Cleanup(s.Close)

	err := s.ApplySkillInvocation(&skill.InvocationResult{
		Spec:       skill.Spec{Name: "debug"},
		Mode:       skill.ModeFork,
		PromptText: "forked prompt",
		Delta: skill.Delta{
			ModelOverride: "other-model",
			Effort:        "high",
		},
	})
	if err != nil {
		t.Fatalf("ApplySkillInvocation error: %v", err)
	}

	if got := s.ModelName(); got != "base-model" {
		t.Fatalf("fork skill should not mutate parent model, got %q", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skillRuntime.invocationCount["debug"] != 1 {
		t.Fatalf("expected fork skill invocation to be recorded, got %#v", s.skillRuntime.invocationCount)
	}
}

func TestApplySkillInvocationQueuesPathHintsWithoutIdleAutoResume(t *testing.T) {
	t.Parallel()

	model := &countingChatModel{}
	ag := agentcore.NewAgent(agentcore.WithModel(model))
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleAssistant, "previous answer"),
	}); err != nil {
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
		Delta: skill.Delta{
			Paths: []string{"internal/skill/**"},
		},
	})
	if err != nil {
		t.Fatalf("ApplySkillInvocation error: %v", err)
	}

	if got := model.Calls(); got != 0 {
		t.Fatalf("expected no idle auto-resume for path hints, got %d model calls", got)
	}

	s.mu.Lock()
	reminders := append([]string(nil), s.runtimeReminders...)
	s.mu.Unlock()
	if len(reminders) != 1 {
		t.Fatalf("expected one queued path reminder, got %#v", reminders)
	}
	if !strings.Contains(reminders[0], "internal/skill/**") {
		t.Fatalf("expected path reminder to mention skill paths, got %q", reminders[0])
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
		memory.CompactionSummary{Summary: "summary"},
		textMessage(agentcore.RoleUser, "latest user message"),
	}
	result := s.injectInvokedSkillContext(msgs)
	if len(result) != 3 {
		t.Fatalf("expected preserved reminder inserted, got %d messages", len(result))
	}
	msg, ok := result[1].(agentcore.Message)
	if !ok {
		t.Fatalf("expected inserted agentcore.Message, got %T", result[1])
	}
	if msg.Metadata["skill_preserve"] != true {
		t.Fatalf("expected skill_preserve metadata, got %#v", msg.Metadata)
	}
	if !strings.Contains(msg.TextContent(), `name="review"`) {
		t.Fatalf("expected skill name in reminder, got %q", msg.TextContent())
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

func TestCompactWithReasonDoesNotEmitEndEventOnFailure(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	if err := ag.SetMessages([]agentcore.AgentMessage{textMessage(agentcore.RoleUser, "need compaction")}); err != nil {
		t.Fatalf("set messages: %v", err)
	}

	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{Provider: "openai", Model: "gpt-test", MaxTurns: 30},
		Cwd:      t.TempDir(),
		CreateModel: func(_ string, _ string, _ string, _ string) (agentcore.ChatModel, error) {
			return nil, errors.New("boom")
		},
	})
	t.Cleanup(s.Close)

	var starts, ends int
	unsub := s.Subscribe(func(ev SessionEvent) {
		switch ev.Type {
		case SEAutoCompactionStart:
			starts++
		case SEAutoCompactionEnd:
			ends++
		}
	})
	t.Cleanup(unsub)

	if _, err := s.context.compactWithReason("threshold"); err == nil {
		t.Fatal("expected compaction to fail")
	}
	if starts != 1 {
		t.Fatalf("expected 1 start event, got %d", starts)
	}
	if ends != 0 {
		t.Fatalf("expected no end event on failure, got %d", ends)
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

func (t *stubTool) Name() string           { return t.name }
func (t *stubTool) Description() string    { return t.desc }
func (t *stubTool) Schema() map[string]any { return nil }
func (t *stubTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return nil, nil
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
	readOnly := s.ToolsByName("read", "glob", "grep", "ls")
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

	// Default provider
	apiKey, baseURL := s.resolveCredentials("openai")
	if apiKey != "openai-key" {
		t.Fatalf("expected openai-key, got %s", apiKey)
	}
	if baseURL != "https://openai.example.com" {
		t.Fatalf("expected https://openai.example.com, got %s", baseURL)
	}

	// Cross-provider resolves its own credentials
	apiKey, baseURL = s.resolveCredentials("anthropic")
	if apiKey != "ant-key" {
		t.Fatalf("expected ant-key, got %s", apiKey)
	}
	if baseURL != "" {
		t.Fatalf("expected empty baseURL for anthropic, got %s", baseURL)
	}

	// Unknown provider returns empty
	apiKey, baseURL = s.resolveCredentials("unknown")
	if apiKey != "" || baseURL != "" {
		t.Fatalf("expected empty for unknown provider, got %s/%s", apiKey, baseURL)
	}
}

func TestSwitchSessionCrossProviderCredentials(t *testing.T) {
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
		t.Fatalf("append: %v", err)
	}
	if err := target.AppendModelChange("anthropic", "claude-sonnet-4-5"); err != nil {
		t.Fatalf("append model change: %v", err)
	}

	var capturedKey, capturedBase string
	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:   ag,
		Store:   current,
		Manager: mgr,
		Settings: config.Resolved{
			Provider: "openai",
			Model:    "gpt-5",
			Providers: map[string]config.ProviderConfig{
				"openai":    {APIKey: "openai-key", BaseURL: "https://openai.example.com"},
				"anthropic": {APIKey: "ant-key"},
			},
			ContextWindow: 128000,
			MaxTurns:      30,
		},
		Cwd: dir,
		CreateModel: func(_, _ string, apiKey, baseURL string) (agentcore.ChatModel, error) {
			capturedKey = apiKey
			capturedBase = baseURL
			return &stubChatModel{}, nil
		},
	})
	t.Cleanup(s.Close)

	if err := s.SwitchSession(target.Header().SessionID); err != nil {
		t.Fatalf("switch session: %v", err)
	}

	// Cross-provider uses anthropic's own credentials.
	if capturedKey != "ant-key" {
		t.Fatalf("expected CreateModel to receive ant-key, got %s", capturedKey)
	}
	if capturedBase != "" {
		t.Fatalf("expected empty baseURL for anthropic, got %s", capturedBase)
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

func TestBuildUserMessagePrependsRuntimeRemindersBeforeStaticReminders(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:     ag,
		Settings:  config.Resolved{MaxTurns: 30},
		Cwd:       t.TempDir(),
		Reminders: []string{"<system-reminder>\n静态提醒\n</system-reminder>"},
	})
	t.Cleanup(s.Close)

	s.queueRuntimeReminder("loop", ReminderRepeatToolCall, "<system-reminder>\n动态提醒\n</system-reminder>")

	msg := s.buildUserMessage(agentcore.TextBlock("用户输入"))
	if len(msg.Content) != 3 {
		t.Fatalf("expected 3 content blocks, got %d", len(msg.Content))
	}
	if got := msg.Content[0].Text; !strings.Contains(got, "动态提醒") {
		t.Fatalf("expected runtime reminder first, got %q", got)
	}
	if got := msg.Content[1].Text; !strings.Contains(got, "静态提醒") {
		t.Fatalf("expected static reminder second, got %q", got)
	}
	if got := msg.Content[2].Text; got != "用户输入" {
		t.Fatalf("expected user input last, got %q", got)
	}

	next := s.buildUserMessage(agentcore.TextBlock("第二次输入"))
	if len(next.Content) != 2 {
		t.Fatalf("expected runtime reminder to be one-shot, got %d blocks", len(next.Content))
	}
	if strings.Contains(next.Content[0].Text, "动态提醒") {
		t.Fatal("runtime reminder should be drained after one injection")
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

	var reminders int
	var kinds []RuntimeReminderKind
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type == SERuntimeReminder {
			reminders++
			kinds = append(kinds, ev.ReminderKind)
		}
	})
	t.Cleanup(unsub)

	args := json.RawMessage(`{"path":"main.go"}`)
	for i := 0; i < repeatedToolCallThreshold; i++ {
		toolID := "read-" + string(rune('a'+i))
		s.handleAgentEvent(agentcore.Event{
			Type:   agentcore.EventToolExecStart,
			ToolID: toolID,
			Tool:   "read",
			Args:   args,
		})
		s.handleAgentEvent(agentcore.Event{
			Type:   agentcore.EventToolExecEnd,
			ToolID: toolID,
			Tool:   "read",
		})
	}

	if reminders != 1 {
		t.Fatalf("expected one runtime reminder event, got %d", reminders)
	}
	if len(kinds) != 1 || kinds[0] != ReminderRepeatToolCall {
		t.Fatalf("expected reminder kind %q, got %#v", ReminderRepeatToolCall, kinds)
	}

	msg := s.buildUserMessage(agentcore.TextBlock("继续"))
	if len(msg.Content) == 0 || !strings.Contains(msg.Content[0].Text, "重复调用同一个工具") {
		t.Fatalf("expected repeated-call reminder, got %#v", msg.Content)
	}
	metrics := s.RuntimeMetrics()
	if metrics.ReminderTotal != 1 {
		t.Fatalf("unexpected metrics after repeated-call reminder: %#v", metrics)
	}
	if metrics.ReminderByKind[ReminderRepeatToolCall] != 1 {
		t.Fatalf("expected reminder-by-kind count for repeat_tool_call, got %#v", metrics.ReminderByKind)
	}
}

func TestRepeatedToolCallDoesNotTriggerWhenInterleaved(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)

	var reminders int
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type == SERuntimeReminder {
			reminders++
		}
	})
	t.Cleanup(unsub)

	args := json.RawMessage(`{"path":"main.go"}`)
	sequence := []string{"read", "grep", "read", "ls", "read", "glob", "read", "bash"}
	for i, toolName := range sequence {
		toolID := "mixed-" + string(rune('a'+i))
		callArgs := args
		if toolName != "read" {
			callArgs = json.RawMessage(`{"pattern":"main"}`)
		}
		s.handleAgentEvent(agentcore.Event{
			Type:   agentcore.EventToolExecStart,
			ToolID: toolID,
			Tool:   toolName,
			Args:   callArgs,
		})
		s.handleAgentEvent(agentcore.Event{
			Type:   agentcore.EventToolExecEnd,
			ToolID: toolID,
			Tool:   toolName,
		})
	}

	if reminders != 0 {
		t.Fatalf("expected no repeated-call reminder for interleaved calls, got %d", reminders)
	}
}

func TestAgentEndDoesNotQueueReminderWithoutAssistantReplyInCurrentTurn(t *testing.T) {
	t.Parallel()

	taskStore := localtools.NewTaskStore()
	taskTools := localtools.NewTaskTools(taskStore, nil)
	taskStore.Create("实现 guard", "补任务闭环检测", "实现中", nil)
	inProgress := localtools.TaskInProgress
	if _, err := taskStore.Update("1", localtools.TaskUpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("update task: %v", err)
	}

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}), agentcore.WithTools(taskTools...))
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "上一轮任务"),
		textMessage(agentcore.RoleAssistant, "已经完成修复，相关改动已处理好。"),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	s := NewSession(SessionConfig{
		Agent:     ag,
		Settings:  config.Resolved{MaxTurns: 30},
		Cwd:       t.TempDir(),
		Tools:     taskTools,
		TaskStore: taskStore,
	})
	t.Cleanup(s.Close)

	var reminders int
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type == SERuntimeReminder {
			reminders++
		}
	})
	t.Cleanup(unsub)

	s.beginTurn()
	s.handleAgentEvent(agentcore.Event{Type: agentcore.EventAgentEnd})

	if reminders != 0 {
		t.Fatalf("expected no unfinished-task reminder without current-turn assistant reply, got %d", reminders)
	}
}

func TestAgentEndDoesNotQueueReminderWhenNoTasksRemain(t *testing.T) {
	t.Parallel()

	taskStore := localtools.NewTaskStore()
	taskTools := localtools.NewTaskTools(taskStore, nil)
	taskStore.Create("实现 guard", "补任务闭环检测", "实现中", nil)
	completed := localtools.TaskCompleted
	if _, err := taskStore.Update("1", localtools.TaskUpdateOpts{Status: &completed}); err != nil {
		t.Fatalf("update task: %v", err)
	}

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}), agentcore.WithTools(taskTools...))
	s := NewSession(SessionConfig{
		Agent:     ag,
		Settings:  config.Resolved{MaxTurns: 30},
		Cwd:       t.TempDir(),
		Tools:     taskTools,
		TaskStore: taskStore,
	})
	t.Cleanup(s.Close)

	var reminders int
	var kinds []RuntimeReminderKind
	unsub := s.Subscribe(func(ev SessionEvent) {
		if ev.Type == SERuntimeReminder {
			reminders++
			kinds = append(kinds, ev.ReminderKind)
		}
	})
	t.Cleanup(unsub)

	s.handleAgentEvent(agentcore.Event{Type: agentcore.EventAgentEnd})

	if reminders != 0 {
		t.Fatalf("expected no runtime reminder when tasks are complete, got %d", reminders)
	}
}

func TestDeliverRuntimeReminderSteersCurrentRun(t *testing.T) {
	t.Parallel()

	model := &scriptedReminderModel{}
	readTool := &stubExecTool{name: "read"}
	ag := agentcore.NewAgent(
		agentcore.WithModel(model),
		agentcore.WithTools(readTool),
		agentcore.WithMaxTurns(10),
	)
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 10},
		Cwd:      t.TempDir(),
		Tools:    []agentcore.Tool{readTool},
	})
	t.Cleanup(s.Close)

	if err := s.Prompt("开始"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		return model.secondCallSawReminder && s.LastAssistantText() == "steered"
	})

	if !model.secondCallSawReminder {
		t.Fatal("expected second model call to see steered runtime reminder")
	}
	if got := s.LastAssistantText(); got != "steered" {
		t.Fatalf("expected steered assistant response, got %q", got)
	}
	if len(s.runtimeReminders) != 0 {
		t.Fatalf("expected no queued runtime reminders after in-run steer, got %d", len(s.runtimeReminders))
	}
}

func TestContinueWithRuntimeReminderAutoContinuesWhenIdle(t *testing.T) {
	t.Parallel()

	model := &scriptedReminderModel{}
	ag := agentcore.NewAgent(agentcore.WithModel(model), agentcore.WithMaxTurns(10))
	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, "初始任务"),
		textMessage(agentcore.RoleAssistant, "任务已完成。"),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 10},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)

	s.continueWithRuntimeReminder(
		"test_reminder:1:0",
		ReminderRepeatToolCall,
		"<system-reminder>\n测试运行时提醒。\n</system-reminder>",
	)
	waitFor(t, time.Second, func() bool {
		return s.LastAssistantText() == "steered"
	})

	if got := s.LastAssistantText(); got != "steered" {
		t.Fatalf("expected auto-continued assistant response, got %q", got)
	}
}

func TestRuntimeMetricsTrackCompactionSavings(t *testing.T) {
	t.Parallel()

	model := &stubChatModel{}
	manager := memory.NewEngine(memory.EngineConfig{
		ContextWindow: 16,
		Strategies: []memory.Strategy{
			memory.NewFullSummary(memory.FullSummaryConfig{
				Model:            model,
				KeepRecentTokens: 1,
			}),
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

	msgs := []agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, strings.Repeat("X", 300)),
		textMessage(agentcore.RoleAssistant, "recent"),
	}
	if err := ag.SetMessages(msgs); err != nil {
		t.Fatalf("set messages: %v", err)
	}

	result, err := s.Compact()
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !result.Changed {
		t.Fatal("expected compaction to change context")
	}

	metrics := s.RuntimeMetrics()
	if metrics.CompactionTotal != 1 {
		t.Fatalf("expected one compaction attempt, got %#v", metrics)
	}
	if metrics.CompactionChanged != 1 {
		t.Fatalf("expected one changed compaction, got %#v", metrics)
	}
	if metrics.CompactionSaved <= 0 {
		t.Fatalf("expected positive compaction savings, got %#v", metrics)
	}
	if metrics.CompactionByKind[CompactionKindFull] != 1 {
		t.Fatalf("expected full compaction count, got %#v", metrics.CompactionByKind)
	}
	if metrics.CompactionSavedByKind[CompactionKindFull] <= 0 {
		t.Fatalf("expected full compaction savings, got %#v", metrics.CompactionSavedByKind)
	}
}

func TestHandleProjectedCompactionUpdatesMetricsAndEvents(t *testing.T) {
	t.Parallel()

	ag := agentcore.NewAgent(agentcore.WithModel(&stubChatModel{}))
	s := NewSession(SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      t.TempDir(),
	})
	t.Cleanup(s.Close)

	var starts, ends int
	unsub := s.Subscribe(func(ev SessionEvent) {
		switch ev.Type {
		case SEAutoCompactionStart:
			starts++
		case SEAutoCompactionEnd:
			ends++
		}
	})
	t.Cleanup(unsub)

	s.HandleProjectedCompaction(memory.ChangeInfo{
		Reason:       "threshold",
		Strategy:     "light_trim",
		Changed:      true,
		TokensBefore: 1000,
		TokensAfter:  600,
	})

	if starts != 1 || ends != 1 {
		t.Fatalf("expected one auto compaction event pair, got starts=%d ends=%d", starts, ends)
	}
	metrics := s.RuntimeMetrics()
	if metrics.CompactionTotal != 1 || metrics.CompactionChanged != 1 {
		t.Fatalf("unexpected compaction metrics: %#v", metrics)
	}
	if metrics.CompactionByKind[CompactionKindTrim] != 1 {
		t.Fatalf("expected trim compaction count, got %#v", metrics.CompactionByKind)
	}
	if metrics.CompactionSavedByKind[CompactionKindTrim] != 400 {
		t.Fatalf("expected trim compaction savings=400, got %#v", metrics.CompactionSavedByKind)
	}
}

func TestSwitchSessionResetsHarnessDiagnostics(t *testing.T) {
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

	model := &stubChatModel{}
	manager := memory.NewEngine(memory.EngineConfig{
		ContextWindow: 16,
		Strategies: []memory.Strategy{
			memory.NewFullSummary(memory.FullSummaryConfig{
				Model:            model,
				KeepRecentTokens: 1,
			}),
		},
	})
	ag := agentcore.NewAgent(agentcore.WithModel(model))
	s := NewSession(SessionConfig{
		Agent:          ag,
		ContextManager: manager,
		Store:          current,
		Manager:        mgr,
		Settings: config.Resolved{
			Provider: "openai",
			Model:    "good-model",
			Providers: map[string]config.ProviderConfig{
				"openai": {APIKey: "k"},
			},
			ContextWindow:  128000,
			AutoCompaction: false,
			MaxTurns:       30,
		},
		Cwd: dir,
	})
	t.Cleanup(s.Close)

	s.beginTurn()
	s.handleAgentEvent(agentcore.Event{
		Type:   agentcore.EventToolExecStart,
		ToolID: "read-1",
		Tool:   "read",
	})
	s.handleAgentEvent(agentcore.Event{
		Type:   agentcore.EventToolExecEnd,
		ToolID: "read-1",
		Tool:   "read",
	})
	s.handleAgentEvent(agentcore.Event{
		Type:    agentcore.EventMessageEnd,
		Message: textMessage(agentcore.RoleAssistant, "已经完成修复。"),
	})
	s.handleAgentEvent(agentcore.Event{Type: agentcore.EventAgentEnd})

	s.queueRuntimeReminder("repeat_tool_call:test", ReminderRepeatToolCall, "<system-reminder>test</system-reminder>")

	if err := ag.SetMessages([]agentcore.AgentMessage{
		textMessage(agentcore.RoleUser, strings.Repeat("X", 300)),
		textMessage(agentcore.RoleAssistant, "recent"),
	}); err != nil {
		t.Fatalf("set messages: %v", err)
	}
	if _, err := s.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}

	if got := len(s.RecentToolCalls(5)); got == 0 {
		t.Fatal("expected recent tool calls before switch")
	}
	if _, ok := s.LastReminder(); !ok {
		t.Fatal("expected last reminder before switch")
	}
	if _, ok := s.LastCompaction(); !ok {
		t.Fatal("expected last compaction before switch")
	}
	if metrics := s.RuntimeMetrics(); metrics.ReminderTotal == 0 || metrics.CompactionTotal == 0 {
		t.Fatalf("expected populated metrics before switch, got %#v", metrics)
	}

	if err := s.SwitchSession(target.Header().SessionID); err != nil {
		t.Fatalf("switch session: %v", err)
	}

	if got := len(s.RecentToolCalls(5)); got != 0 {
		t.Fatalf("expected recent tool calls reset after switch, got %d", got)
	}
	if _, ok := s.LastReminder(); ok {
		t.Fatal("expected last reminder reset after switch")
	}
	if _, ok := s.LastCompaction(); ok {
		t.Fatal("expected last compaction reset after switch")
	}
	if got := s.LastTurnOutcome(); got != (TurnOutcomeSnapshot{}) {
		t.Fatalf("expected last turn reset after switch, got %#v", got)
	}
	metrics := s.RuntimeMetrics()
	if metrics.ReminderTotal != 0 || metrics.CompactionTotal != 0 || metrics.CompactionChanged != 0 || metrics.CompactionSaved != 0 {
		t.Fatalf("expected zeroed metrics after switch, got %#v", metrics)
	}
	if len(metrics.ReminderByKind) != 0 || len(metrics.CompactionByKind) != 0 || len(metrics.CompactionSavedByKind) != 0 {
		t.Fatalf("expected empty metric breakdowns after switch, got %#v", metrics)
	}
}
