package approval

import (
	"context"
	"encoding/json"
	"os"
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

	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
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

	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
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

func TestBalancedWriteOutsideWorkspaceIsDenied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	engine, err := NewEngine(workspace, ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"path": outside})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "write", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatalf("write outside workspace should be denied, got %#v", result)
	}
}

func TestBalancedReadOutsideWorkspaceIsDenied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	engine, err := NewEngine(workspace, ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"path": outside})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "read", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatalf("read outside workspace should be denied, got %#v", result)
	}
}

func TestBalancedReadInExtraReadRootIsAllowed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	workspace := t.TempDir()
	extraReadRoot := t.TempDir()
	engine, err := NewEngine(workspace, ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetFilesystemRoots(FilesystemRoots{
		ReadRoots:  []string{workspace, extraReadRoot},
		WriteRoots: []string{workspace},
	})

	args, _ := json.Marshal(map[string]any{"path": filepath.Join(extraReadRoot, "outside.txt")})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "read", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result != nil {
		t.Fatalf("read in extra read root should bypass approval, got %#v", result)
	}
}

func TestBalancedWriteInReadOnlyRootIsDenied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	workspace := t.TempDir()
	extraReadRoot := t.TempDir()
	engine, err := NewEngine(workspace, ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetFilesystemRoots(FilesystemRoots{
		ReadRoots:  []string{workspace, extraReadRoot},
		WriteRoots: []string{workspace},
	})
	engine.SetApprover(func(context.Context, Prompt) (Choice, error) {
		t.Fatal("approver should not be called for read-only external root")
		return ChoiceAllowOnce, nil
	})

	args, _ := json.Marshal(map[string]any{"path": filepath.Join(extraReadRoot, "blocked.txt")})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "write", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatalf("write in read-only external root should be denied, got %#v", result)
	}
}

func TestBalancedWriteInExtraWriteRootUsesApprovalFlow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	workspace := t.TempDir()
	extraWriteRoot := t.TempDir()
	engine, err := NewEngine(workspace, ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetFilesystemRoots(FilesystemRoots{
		ReadRoots:  []string{workspace, extraWriteRoot},
		WriteRoots: []string{workspace, extraWriteRoot},
	})

	var prompts int
	engine.SetApprover(func(context.Context, Prompt) (Choice, error) {
		prompts++
		return ChoiceAllowOnce, nil
	})

	args, _ := json.Marshal(map[string]any{"path": filepath.Join(extraWriteRoot, "allowed.txt")})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "write", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || !result.Approved {
		t.Fatalf("write in extra write root should be approvable, got %#v", result)
	}
	if prompts != 1 {
		t.Fatalf("expected one approval prompt, got %d", prompts)
	}
}

func TestOutsideRootsAllowSessionDegradesToAllowOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	engine, err := NewEngine(workspace, ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	var prompts int
	engine.SetApprover(func(_ context.Context, p Prompt) (Choice, error) {
		prompts++
		if !p.OutsideRoots {
			t.Fatal("expected OutsideRoots=true in prompt")
		}
		// User tries to allow session — engine should degrade this.
		return ChoiceAllowSession, nil
	})

	// First call: outside roots read, user says "allow session".
	args, _ := json.Marshal(map[string]any{"path": outside})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "read", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || !result.Approved {
		t.Fatalf("first outside read should be approved (once), got %#v", result)
	}

	// Second call: same outside path — must prompt again (not cached).
	result, err = engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "read", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool second: %v", err)
	}
	if prompts != 2 {
		t.Fatalf("expected 2 approval prompts (no caching), got %d", prompts)
	}
}

func TestBalancedBashWorkdirOutsideRootsRequiresApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	workspace := t.TempDir()
	engine, err := NewEngine(workspace, ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"command": "ls", "workdir": "/tmp/elsewhere"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatalf("bash with workdir outside roots should be denied, got %#v", result)
	}
}

func TestBalancedBashWorkdirInWriteRootIsAllowed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	workspace := t.TempDir()
	extraDir := t.TempDir()
	engine, err := NewEngine(workspace, ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetFilesystemRoots(FilesystemRoots{
		ReadRoots:  []string{workspace, extraDir},
		WriteRoots: []string{workspace, extraDir},
	})

	var prompts int
	engine.SetApprover(func(context.Context, Prompt) (Choice, error) {
		prompts++
		return ChoiceAllowOnce, nil
	})

	args, _ := json.Marshal(map[string]any{"command": "ls", "workdir": extraDir})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || !result.Approved {
		t.Fatalf("bash with workdir in write root should be approvable, got %#v", result)
	}
	if prompts != 1 {
		t.Fatalf("expected one approval prompt, got %d", prompts)
	}
}

func TestBalancedWriteViaSymlinkEscapeIsDenied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	workspace := t.TempDir()
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("MkdirAll outside: %v", err)
	}
	link := filepath.Join(workspace, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	engine, err := NewEngine(workspace, ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"path": "link/escape.txt"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "write", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatalf("write through symlink escape should be denied, got %#v", result)
	}
}

func TestPlanModeDeniesWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetPlanMode(true)

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

func TestPlanModeStillDeniesWriteInTrustMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetMode(ModeTrust)
	engine.SetPlanMode(true)

	args, _ := json.Marshal(map[string]any{"path": "a.txt"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "write", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatalf("plan mode should stay read-only even in trust mode, got %#v", result)
	}
}

func TestAllowAlwaysSkipsFutureApproval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()

	engine, err := NewEngine(workspace, ModeBalanced, nil, nil)
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

	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
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

	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetPlanMode(true)

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

	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetPlanMode(true)

	if err := engine.ApproveCommand(context.Background(), CommandRequest{
		Name:     "help",
		Category: CommandCategoryInfo,
	}); err != nil {
		t.Fatalf("expected info command to pass in plan mode: %v", err)
	}
}

func TestReplaceToolMetadataClassifiesDynamicTool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	engine, err := NewEngine(t.TempDir(), ModeBalanced, nil, nil)
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

func TestSkillAllowsAutoApprovesTool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ws := t.TempDir()
	engine, err := NewEngine(ws, ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Bash normally requires approval under balanced mode.
	args, _ := json.Marshal(map[string]any{"command": "echo hello"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatal("bash should require approval before skill allows")
	}

	// Set skill allows for Bash.
	engine.SetSkillAllows([]string{"Bash"})

	result, err = engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool after skill allows: %v", err)
	}
	if result != nil {
		t.Fatalf("bash should be auto-approved with skill allows, got %#v", result)
	}

	// Clear skill allows — should require approval again.
	engine.SetSkillAllows(nil)

	result, err = engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool after clear: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatal("bash should require approval after clearing skill allows")
	}
}

func TestSkillAllowsDenyRuleTakesPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ws := t.TempDir()
	rules, _ := ParseRuleSet(nil, []string{"Bash"})
	engine, err := NewEngine(ws, ModeBalanced, rules, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Even with skill allows, deny rules win.
	engine.SetSkillAllows([]string{"Bash"})

	args, _ := json.Marshal(map[string]any{"command": "echo hello"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatal("deny rule should take precedence over skill allows")
	}
}
