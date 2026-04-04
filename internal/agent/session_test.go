package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/skill"
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
	if !strings.Contains(msg.Content[0].Text, "动态提醒") || !strings.Contains(msg.Content[1].Text, "静态提醒") {
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

	args := json.RawMessage(`{"path":"main.go"}`)
	for i := 0; i < repeatedToolCallThreshold; i++ {
		toolID := "read-" + string(rune('a'+i))
		s.handleAgentEvent(agentcore.Event{Type: agentcore.EventToolExecStart, ToolID: toolID, Tool: "read", Args: args})
		s.handleAgentEvent(agentcore.Event{Type: agentcore.EventToolExecEnd, ToolID: toolID, Tool: "read"})
	}

	msg := s.buildUserMessage(agentcore.TextBlock("继续"))
	if len(msg.Content) == 0 || !strings.Contains(msg.Content[0].Text, "重复调用同一个工具") {
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

	if err := s.Prompt("开始"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		return model.secondCallSawReminder && s.LastAssistantText() == "steered"
	})
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

	s.continueWithRuntimeReminder("test_reminder:1:0", ReminderRepeatToolCall, "<system-reminder>\n测试运行时提醒。\n</system-reminder>")
	waitFor(t, time.Second, func() bool {
		return s.LastAssistantText() == "steered"
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
