package plan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/permission"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/storage"
	localtools "github.com/voocel/codebot/internal/tools"
)

type stubModel struct{}

func (m *stubModel) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return &agentcore.LLMResponse{Message: agentcore.Message{Role: agentcore.RoleAssistant}}, nil
}

func (m *stubModel) GenerateStream(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent, 1)
	close(ch)
	return ch, nil
}

func (m *stubModel) SupportsTools() bool { return true }

type stubTool struct {
	name string
}

func (t *stubTool) Name() string           { return t.name }
func (t *stubTool) Description() string    { return t.name }
func (t *stubTool) Schema() map[string]any { return nil }
func (t *stubTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func TestControllerEnterSubmitApprove(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := storage.NewManager(dir).Create(dir)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	defer store.Close()

	engine, err := approval.NewEngine(dir, approval.ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	ag := agentcore.NewAgent(agentcore.WithModel(&stubModel{}))
	session := agent.NewSession(agent.SessionConfig{
		Agent: ag,
		Settings: config.Resolved{
			MaxTurns: 30,
		},
		Cwd: dir,
		Tools: []agentcore.Tool{
			&stubTool{name: "read"},
			&stubTool{name: "glob"},
			&stubTool{name: "grep"},
			&stubTool{name: "ls"},
			&stubTool{name: "ask_user"},
		},
	})
	defer session.Close()

	planStore := storage.NewPlanStore(dir)
	controller := NewManager(session, engine, planStore, store)
	if err := controller.Restore(State{Phase: PhaseOff}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if _, err := controller.Enter("refactor auth"); err != nil {
		t.Fatalf("enter: %v", err)
	}
	state := controller.Snapshot()
	if state.Phase != PhasePlanning {
		t.Fatalf("phase = %q, want planning", state.Phase)
	}
	if controller.CurrentPlanPath() == "" {
		t.Fatal("expected plan path after enter")
	}

	// Simulate the model writing the plan to the plan file via the write tool.
	if err := planStore.Save(state.Slug, "# Auth Plan\n\n- step 1"); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	commands := []AllowedCommand{{CommandPrefix: "go test", Description: "运行单元测试"}}
	if err := controller.Submit("Auth Plan", commands); err != nil {
		t.Fatalf("submit: %v", err)
	}
	state = controller.Snapshot()
	if state.Phase != PhaseReview {
		t.Fatalf("phase = %q, want review", state.Phase)
	}

	title, content, actions, err := controller.Approve()
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if title != "Auth Plan" {
		t.Fatalf("title = %q, want Auth Plan", title)
	}
	if !strings.Contains(content, "# Auth Plan") {
		t.Fatalf("unexpected content: %q", content)
	}
	if len(actions) != 1 || actions[0].CommandPrefix != "go test" {
		t.Fatalf("unexpected actions: %#v", actions)
	}
	if controller.Snapshot().Phase != PhaseOff {
		t.Fatalf("phase = %q, want off", controller.Snapshot().Phase)
	}
	if !strings.Contains(ag.State().SystemPrompt, "[APPROVED PLAN]") {
		t.Fatalf("expected approved plan overlay, got %q", ag.State().SystemPrompt)
	}
	if !strings.Contains(ag.State().SystemPrompt, "Allowed command prefixes for this session") {
		t.Fatalf("expected approved commands heading in prompt, got %q", ag.State().SystemPrompt)
	}
	if !strings.Contains(ag.State().SystemPrompt, "go test (运行单元测试)") {
		t.Fatalf("expected approved commands in prompt, got %q", ag.State().SystemPrompt)
	}

	decision, err := engine.Decide(context.Background(), permission.Request{
		ToolName: "bash",
		Args:     json.RawMessage(`{"command":"go test ./..."}`),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision == nil || !decision.Allowed() {
		t.Fatalf("expected approved plan action to allow tests, got %#v", decision)
	}
}

// TestPlanModeKeepsToolListAndDelegatesToPermission locks the Claude Code
// contract: plan mode does NOT swap out the session's tool list. The
// permission engine alone enforces read-only semantics. Any future
// regression that strips write/task_create/subagent from session.Tools when
// entering plan mode will fail the first assertion.
func TestPlanModeKeepsToolListAndDelegatesToPermission(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := storage.NewManager(dir).Create(dir)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	defer store.Close()

	engine, err := approval.NewEngine(dir, approval.ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	allTools := []agentcore.Tool{
		&stubTool{name: "write"},
		&stubTool{name: "edit"},
		&stubTool{name: "task_create"},
		&stubTool{name: "subagent"},
		&stubTool{name: "read"},
		&stubTool{name: "grep"},
		&stubTool{name: "glob"},
		&stubTool{name: "ls"},
		&stubTool{name: "bash"},
		&stubTool{name: "ask_user"},
	}

	ag := agentcore.NewAgent(agentcore.WithModel(&stubModel{}), agentcore.WithTools(allTools...))
	session := agent.NewSession(agent.SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      dir,
		Tools:    allTools,
	})
	defer session.Close()

	planStore := storage.NewPlanStore(dir)
	controller := NewManager(session, engine, planStore, store)
	if err := controller.Restore(State{Phase: PhaseOff}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := controller.Enter("explore"); err != nil {
		t.Fatalf("enter: %v", err)
	}

	for _, name := range []string{"write", "edit", "task_create", "subagent"} {
		if got := len(session.ToolsByName(name)); got == 0 {
			t.Fatalf("tool %q must remain in session.Tools during plan mode (Claude Code contract); was removed", name)
		}
	}

	planPath := controller.CurrentPlanPath()
	if planPath == "" {
		t.Fatal("expected plan path after enter")
	}
	otherPath := dir + "/main.go"

	denyCases := []permission.Request{
		{ToolName: "write", Args: json.RawMessage(`{"path":"` + otherPath + `","content":"x"}`)},
		{ToolName: "edit", Args: json.RawMessage(`{"path":"` + otherPath + `"}`)},
		{ToolName: "task_create"},
		{ToolName: "subagent"},
	}
	for _, req := range denyCases {
		decision, err := engine.Decide(context.Background(), req)
		if err != nil {
			t.Fatalf("decide %s: %v", req.ToolName, err)
		}
		if decision == nil || decision.Allowed() {
			t.Fatalf("permission engine must deny %q in plan mode, got %#v", req.ToolName, decision)
		}
	}

	// The plan file is the ONE writable path during planning. Verifies the
	// approval.Engine.SetPlanFilePath wiring and the agentcore
	// PlanModeWriteAllowed hook are connected end-to-end.
	allowCases := []permission.Request{
		{ToolName: "read", Args: json.RawMessage(`{"path":"main.go"}`)},
		{ToolName: "grep", Args: json.RawMessage(`{"pattern":"foo"}`)},
		{ToolName: "bash", Args: json.RawMessage(`{"command":"grep -r foo ."}`)},
		{ToolName: "ask_user"},
		{ToolName: "write", Args: json.RawMessage(`{"path":"` + planPath + `","content":"# Plan"}`)},
		{ToolName: "edit", Args: json.RawMessage(`{"path":"` + planPath + `"}`)},
	}
	for _, req := range allowCases {
		decision, err := engine.Decide(context.Background(), req)
		if err != nil {
			t.Fatalf("decide %s: %v", req.ToolName, err)
		}
		if decision == nil || !decision.Allowed() {
			t.Fatalf("permission engine must allow %q in plan mode, got %#v", req.ToolName, decision)
		}
	}

	// After cancel, plan file path is cleared — the previous allowance must
	// stop applying. This guards against a stale planFilePath surviving phase
	// transitions (which would let the model keep editing the plan after
	// approval / cancel).
	if err := controller.Cancel(); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got := engine.PlanFilePath(); got != "" {
		t.Fatalf("plan file path must clear on cancel, still got %q", got)
	}
}

func TestEnterPlanToolSynchronouslyEntersPlanMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := storage.NewManager(dir).Create(dir)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	defer store.Close()

	engine, err := approval.NewEngine(dir, approval.ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	enterTool := localtools.NewEnterPlanMode()
	ag := agentcore.NewAgent(agentcore.WithModel(&stubModel{}), agentcore.WithTools(enterTool))
	session := agent.NewSession(agent.SessionConfig{
		Agent: ag,
		Settings: config.Resolved{
			MaxTurns: 30,
		},
		Cwd:   dir,
		Tools: []agentcore.Tool{enterTool},
	})
	defer session.Close()

	controller := NewManager(session, engine, storage.NewPlanStore(dir), store)
	if err := controller.Restore(State{Phase: PhaseOff}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	result, err := enterTool.Execute(context.Background(), json.RawMessage(`{"task":"build novel cli"}`))
	if err != nil {
		t.Fatalf("enter tool execute: %v", err)
	}
	if controller.Snapshot().Phase != PhasePlanning {
		t.Fatalf("phase = %q, want planning", controller.Snapshot().Phase)
	}
	if !strings.Contains(string(result), "Plan file:") {
		t.Fatalf("expected plan file path in result prompt, got %s", result)
	}
	if !strings.Contains(string(result), "write or edit tool") {
		t.Fatalf("expected write/edit tool guidance in result prompt, got %s", result)
	}
}
