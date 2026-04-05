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

	commands := []AllowedCommand{{CommandPrefix: "go test", Description: "运行单元测试"}}
	if err := controller.Submit("Auth Plan", "# Auth Plan\n\n- step 1", commands); err != nil {
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
