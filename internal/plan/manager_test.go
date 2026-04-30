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

func newTestSession(t *testing.T, dir string, tools []agentcore.Tool) (*agent.Session, *agentcore.Agent) {
	t.Helper()
	ag := agentcore.NewAgent(agentcore.WithModel(&stubModel{}), agentcore.WithTools(tools...))
	session := agent.NewSession(agent.SessionConfig{
		Agent:    ag,
		Settings: config.Resolved{MaxTurns: 30},
		Cwd:      dir,
		Tools:    tools,
	})
	return session, ag
}

// TestExitPlanModeApprovedFlow drives the full CC-style lifecycle: enter
// plan mode, the model writes the plan, exit_plan_mode is intercepted by
// engine.Decide which surfaces the plan to the approver, the approver
// approves, the tool runs and unwinds plan mode.
func TestExitPlanModeApprovedFlow(t *testing.T) {
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
	var promptedWith string
	engine.SetApprover(func(_ context.Context, p approval.Prompt) (approval.Choice, error) {
		promptedWith = p.Preview
		return approval.ChoiceAllowOnce, nil
	})

	enterTool := localtools.NewEnterPlanMode()
	exitTool := localtools.NewExitPlanMode()
	session, _ := newTestSession(t, dir, []agentcore.Tool{enterTool, exitTool})
	defer session.Close()

	planStore := storage.NewPlanStore(dir)
	controller := NewManager(session, engine, planStore, store)
	if err := controller.Restore(State{Phase: PhaseOff}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if _, err := controller.Enter(); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := planStore.Save(controller.Snapshot().Slug, "# Auth Plan\n\n- step 1"); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	// Drive the permission gate first — this is the path that the agent
	// loop takes before invoking the tool. exit_plan_mode + plan mode is
	// intercepted in Engine.Decide and routed through the approver.
	decision, err := engine.Decide(context.Background(), permission.Request{
		ToolName: "exit_plan_mode",
		Args:     json.RawMessage(`{"allowed_prompts":[{"tool":"Bash","prompt":"go test"}]}`),
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if decision == nil || !decision.Allowed() {
		t.Fatalf("expected allow decision, got %#v", decision)
	}
	if !strings.Contains(promptedWith, "# Auth Plan") {
		t.Fatalf("expected plan content in approver preview, got %q", promptedWith)
	}
	if !strings.Contains(promptedWith, "Bash: go test") {
		t.Fatalf("expected allowed_prompts in preview, got %q", promptedWith)
	}

	// With approval granted, the tool's Execute transitions state.
	result, err := exitTool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("exit tool execute: %v", err)
	}
	if !strings.Contains(string(result), "Auth Plan") {
		t.Fatalf("expected plan in tool result, got %s", result)
	}
	if controller.Snapshot().Phase != PhaseOff {
		t.Fatalf("phase = %q, want off", controller.Snapshot().Phase)
	}
}

// TestExitPlanModeDeniedKeepsPlanModeActive verifies that engine.Decide
// returns Deny on user denial, and plan mode stays active so the model can
// refine and retry.
func TestExitPlanModeDeniedKeepsPlanModeActive(t *testing.T) {
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
	engine.SetApprover(func(_ context.Context, _ approval.Prompt) (approval.Choice, error) {
		return approval.ChoiceDeny, nil
	})

	session, _ := newTestSession(t, dir, []agentcore.Tool{&stubTool{name: "read"}})
	defer session.Close()

	planStore := storage.NewPlanStore(dir)
	controller := NewManager(session, engine, planStore, store)
	if err := controller.Restore(State{Phase: PhaseOff}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := controller.Enter(); err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := planStore.Save(controller.Snapshot().Slug, "# Plan"); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	decision, err := engine.Decide(context.Background(), permission.Request{
		ToolName: "exit_plan_mode",
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if decision == nil || decision.Allowed() {
		t.Fatalf("expected deny decision, got %#v", decision)
	}
	if controller.Snapshot().Phase != PhasePlanning {
		t.Fatalf("phase = %q, want planning after denial", controller.Snapshot().Phase)
	}
}

// TestPlanModeKeepsToolListAndDelegatesToPermission locks the contract that
// plan mode does NOT swap out the session's tool list. The permission
// engine alone enforces read-only semantics. Any future regression that
// strips write/task_create/subagent from session.Tools when entering plan
// mode will fail the first assertion.
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

	session, _ := newTestSession(t, dir, allTools)
	defer session.Close()

	planStore := storage.NewPlanStore(dir)
	controller := NewManager(session, engine, planStore, store)
	if err := controller.Restore(State{Phase: PhaseOff}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := controller.Enter(); err != nil {
		t.Fatalf("enter: %v", err)
	}

	for _, name := range []string{"write", "edit", "task_create", "subagent"} {
		if got := len(session.ToolsByName(name)); got == 0 {
			t.Fatalf("tool %q must remain in session.Tools during plan mode; was removed", name)
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

	if err := controller.Cancel(); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got := engine.PlanFilePath(); got != "" {
		t.Fatalf("plan file path must clear on cancel, still got %q", got)
	}
}

// TestEnterPlanToolSynchronouslyEntersPlanMode verifies enter_plan_mode
// performs the state transition inside its handler so the next turn already
// sees PlanMode=true.
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
	session, _ := newTestSession(t, dir, []agentcore.Tool{enterTool})
	defer session.Close()

	controller := NewManager(session, engine, storage.NewPlanStore(dir), store)
	if err := controller.Restore(State{Phase: PhaseOff}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	result, err := enterTool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("enter tool execute: %v", err)
	}
	if controller.Snapshot().Phase != PhasePlanning {
		t.Fatalf("phase = %q, want planning", controller.Snapshot().Phase)
	}
	if !strings.Contains(string(result), controller.CurrentPlanPath()) {
		t.Fatalf("expected plan file path in result prompt, got %s", result)
	}
}

// TestPlanExitWithoutApproverFallsThroughToAllow ensures headless / scripted
// flows (no approver wired) don't deadlock — the engine falls through to
// allow so the tool can complete. Audit notes the missing approver.
func TestPlanExitWithoutApproverFallsThroughToAllow(t *testing.T) {
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

	session, _ := newTestSession(t, dir, []agentcore.Tool{&stubTool{name: "read"}})
	defer session.Close()

	planStore := storage.NewPlanStore(dir)
	controller := NewManager(session, engine, planStore, store)
	if err := controller.Restore(State{Phase: PhaseOff}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := controller.Enter(); err != nil {
		t.Fatalf("enter: %v", err)
	}

	decision, err := engine.Decide(context.Background(), permission.Request{ToolName: "exit_plan_mode"})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if decision == nil || !decision.Allowed() {
		t.Fatalf("expected allow fallback when no approver is wired, got %#v", decision)
	}
}
