package approval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/voocel/agentcore"
)

type metadataTool struct {
	name string
	meta ToolMetadata
}

func (t metadataTool) Name() string           { return t.name }
func (t metadataTool) Description() string    { return "" }
func (t metadataTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t metadataTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}
func (t metadataTool) ApprovalMetadata() ToolMetadata { return t.meta }

func TestBalancedReadDoesNotNeedApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"path": "a.txt"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "read", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result != nil {
		t.Fatalf("read should bypass approval, got %#v", result)
	}
}

func TestBalancedWriteNeedsApprovalWithoutApprover(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"path": "a.txt"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "write", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatalf("write should be denied without approver, got %#v", result)
	}
}

func TestPlanModeDeniesWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetMode(ModePlan)

	args, _ := json.Marshal(map[string]any{"path": "a.txt"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "write", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatalf("plan mode should deny writes, got %#v", result)
	}
}

func TestAllowAlwaysSkipsFutureApproval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()

	engine, err := NewEngine(workspace, ProfileBalanced, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	var prompts int
	engine.SetApprover(func(ctx context.Context, prompt Prompt) (Choice, error) {
		prompts++
		return ChoiceAllowAlways, nil
	})

	args, _ := json.Marshal(map[string]any{"path": "nested/a.txt"})
	first, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "write", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool first: %v", err)
	}
	if first == nil || !first.Approved {
		t.Fatalf("first approval should pass, got %#v", first)
	}
	if prompts != 1 {
		t.Fatalf("expected one approval prompt, got %d", prompts)
	}

	engine.SetApprover(nil)
	second, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "write", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool second: %v", err)
	}
	if second != nil {
		t.Fatalf("stored approval should bypass prompt, got %#v", second)
	}

	path := filepath.Join(home, ".codebot", "approvals")
	matches, err := filepath.Glob(filepath.Join(path, "*.json"))
	if err != nil {
		t.Fatalf("glob approvals: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("expected persisted approval file under %s", path)
	}
}

func TestApproveHookNeedsApprovalWithoutApprover(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	err = engine.ApproveHook(context.Background(), HookRequest{
		Event:   "PreToolUse",
		Tool:    "bash",
		Command: "echo ok",
	})
	if err == nil {
		t.Fatal("expected hook approval to fail without approver")
	}
}

func TestApproveCommandBlocksSessionCommandInPlanMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetMode(ModePlan)

	err = engine.ApproveCommand(context.Background(), CommandRequest{
		Name:     "new",
		Category: CommandCategorySession,
	})
	if err == nil {
		t.Fatal("expected plan mode to block session command")
	}
}

func TestApproveCommandAllowsInfoCommandInPlanMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetMode(ModePlan)

	if err := engine.ApproveCommand(context.Background(), CommandRequest{
		Name:     "help",
		Category: CommandCategoryInfo,
	}); err != nil {
		t.Fatalf("expected info command to pass in plan mode: %v", err)
	}
}

func TestReplaceToolMetadataClassifiesDynamicTool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.ReplaceToolMetadata([]agentcore.Tool{
		metadataTool{
			name: "mcp__srv__read_docs",
			meta: ToolMetadata{
				ToolName:   "mcp__srv__read_docs",
				Capability: CapRead,
				KeyPrefix:  "mcp",
			},
		},
	})

	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "mcp__srv__read_docs"},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result != nil {
		t.Fatalf("read-only dynamic tool should bypass approval, got %#v", result)
	}
}
