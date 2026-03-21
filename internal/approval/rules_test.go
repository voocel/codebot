package approval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/agentcore"
)

func TestParseRule(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw     string
		kind    string
		pattern string
	}{
		{"Bash(npm test *)", "Bash", "npm test *"},
		{"Edit(src/**)", "Edit", "src/**"},
		{"Read(~/.ssh/*)", "Read", "~/.ssh/*"},
		{"WebFetch(*.github.com)", "WebFetch", "*.github.com"},
		{"Subagent(researcher)", "Subagent", "researcher"},
		{"mcp__ctx7__*", "tool", "mcp__ctx7__*"},
	}
	for _, tc := range cases {
		r, err := ParseRule(tc.raw)
		if err != nil {
			t.Fatalf("ParseRule(%q): %v", tc.raw, err)
		}
		if r.Kind != tc.kind || r.Pattern != tc.pattern {
			t.Fatalf("ParseRule(%q) = {Kind:%q Pattern:%q}, want {Kind:%q Pattern:%q}",
				tc.raw, r.Kind, r.Pattern, tc.kind, tc.pattern)
		}
	}
}

func TestParseRuleInvalid(t *testing.T) {
	t.Parallel()

	invalid := []string{"", "Unknown(foo)", "Bash("}
	for _, raw := range invalid {
		if _, err := ParseRule(raw); err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}

func TestParseRuleSetNilForEmpty(t *testing.T) {
	t.Parallel()

	rs, err := ParseRuleSet(nil, nil)
	if err != nil || rs != nil {
		t.Fatalf("empty input should return nil, got %v %v", rs, err)
	}
}

func TestDenyRuleBlocksBash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet(nil, []string{"Bash(rm -rf *)"})
	if err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, rs, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine.SetApprover(func(context.Context, Prompt) (Choice, error) {
		t.Fatal("approver should not be called for denied commands")
		return ChoiceDeny, nil
	})

	args, _ := json.Marshal(map[string]any{"command": "rm -rf /"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatalf("deny rule should block rm, got %#v", result)
	}
}

func TestAllowRuleSkipsApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet([]string{"Bash(go test *)"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, rs, nil)
	if err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{"command": "go test ./..."})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result != nil {
		t.Fatalf("allow rule should bypass approval, got %#v", result)
	}
}

func TestDenyRuleOverridesCachedApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet(nil, []string{"Bash(git push *)"})
	if err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, rs, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a cached approval by calling AllowAlways first on a different command,
	// then add the same key manually.
	args, _ := json.Marshal(map[string]any{"command": "git push origin main"})
	info := inspectCall(engine.workspace, agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	engine.sessionAllow[info.key] = storedEntry{Key: info.key}

	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatalf("deny rule should override cached approval, got %#v", result)
	}
}

func TestNoMatchFallsBackToCapability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet([]string{"Bash(npm *)"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, rs, nil)
	if err != nil {
		t.Fatal(err)
	}

	// "go test" doesn't match "npm *", should fall through to capability logic (ask).
	args, _ := json.Marshal(map[string]any{"command": "go test ./..."})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	// Without approver, balanced profile denies bash.
	if result == nil || result.Approved {
		t.Fatalf("unmatched rule should fall back to capability (deny without approver), got %#v", result)
	}
}

func TestDenyTakesPrecedenceOverAllow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet(
		[]string{"Bash(git *)"},
		[]string{"Bash(git push *)"},
	)
	if err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, rs, nil)
	if err != nil {
		t.Fatal(err)
	}

	// "git diff" matches allow, not deny -> allowed.
	diffArgs, _ := json.Marshal(map[string]any{"command": "git diff"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: diffArgs},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("git diff should be allowed, got %#v", result)
	}

	// "git push origin main" matches both deny and allow -> deny wins.
	pushArgs, _ := json.Marshal(map[string]any{"command": "git push origin main"})
	result, err = engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: pushArgs},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Approved {
		t.Fatalf("git push should be denied, got %#v", result)
	}
}

func TestBashCompoundCommandNotAllowed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet([]string{"Bash(echo *)"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, rs, nil)
	if err != nil {
		t.Fatal(err)
	}

	// "echo ok && rm -rf /" should NOT match allow rule (compound command safety).
	args, _ := json.Marshal(map[string]any{"command": "echo ok && rm -rf /"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Approved {
		t.Fatalf("compound command should not match allow rule, got %#v", result)
	}
}

func TestBashDenyMatchesCompoundSubcommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet(nil, []string{"Bash(rm *)"})
	if err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, rs, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine.SetApprover(func(context.Context, Prompt) (Choice, error) {
		return ChoiceAllowOnce, nil
	})

	// "echo ok && rm -rf /" -> deny rule should catch the "rm" segment.
	args, _ := json.Marshal(map[string]any{"command": "echo ok && rm -rf /"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Approved {
		t.Fatalf("deny rule should catch rm in compound command, got %#v", result)
	}
}

func TestEditGlobMatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet([]string{"Edit(src/**)"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	engine, err := NewEngine(workspace, ProfileBalanced, rs, nil)
	if err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{"path": "src/main.go"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call:    agentcore.ToolCall{Name: "write", Args: args},
		Summary: "src/main.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("Edit(src/**) should allow write to src/main.go, got %#v", result)
	}
}

func TestWebFetchHostMatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet([]string{"WebFetch(*.github.com)"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, rs, nil)
	if err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{"url": "https://api.github.com/repos"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "web_fetch", Args: args},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("WebFetch(*.github.com) should allow api.github.com, got %#v", result)
	}
}

func TestToolNameWildcard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet([]string{"mcp__context7__*"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, rs, nil)
	if err != nil {
		t.Fatal(err)
	}

	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "mcp__context7__query"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("wildcard tool name should match, got %#v", result)
	}
}

func TestMatchBashSimple(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pattern string
		command string
		isDeny  bool
		want    bool
	}{
		{"npm test *", "npm test --watch", false, true},
		{"npm test *", "npm test", false, false},
		{"make build", "make build", false, true},
		{"make build", "make build test", false, false},
		{"git *", "git status", false, true},
		{"*", "anything", false, true},
		// Compound commands.
		{"echo *", "echo ok && rm -rf /", false, false}, // allow: no match
		{"rm *", "echo ok && rm -rf /", true, true},      // deny: match subcommand
		// Quoted operators should not split.
		{"python *", `python -c 'print("a|b")'`, false, true},     // allow: pipe inside quotes is not an operator
		{"grep *", `grep "foo && bar" file.txt`, false, true},      // allow: && inside quotes is not an operator
		{"echo *", `echo 'hello;world'`, false, true},              // allow: semicolon inside quotes
	}
	for _, tc := range cases {
		got := matchBash(tc.pattern, tc.command, tc.isDeny)
		if got != tc.want {
			t.Errorf("matchBash(%q, %q, isDeny=%v) = %v, want %v",
				tc.pattern, tc.command, tc.isDeny, got, tc.want)
		}
	}
}

func TestMatchHost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"*.github.com", "api.github.com", true},
		{"*.github.com", "github.com", true},
		{"example.com", "example.com", true},
		{"example.com", "api.example.com", false},
		{"*", "anything.com", true},
	}
	for _, tc := range cases {
		got := matchHost(tc.pattern, tc.host)
		if got != tc.want {
			t.Errorf("matchHost(%q, %q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
		}
	}
}

func TestMatchToolName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"mcp__ctx__*", "mcp__ctx__query", true},
		{"mcp__ctx__*", "mcp__other__query", false},
		{"exact_tool", "exact_tool", true},
		{"exact_tool", "other_tool", false},
	}
	for _, tc := range cases {
		got := matchToolName(tc.pattern, tc.name)
		if got != tc.want {
			t.Errorf("matchToolName(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestDenyRuleEnforcedEvenWithProfileOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet(nil, []string{"Bash(rm -rf *)"})
	if err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(t.TempDir(), ProfileOff, rs, nil)
	if err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{"command": "rm -rf /"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: args},
	})
	if err != nil {
		t.Fatalf("ApproveTool: %v", err)
	}
	if result == nil || result.Approved {
		t.Fatalf("deny rule should still block with ProfileOff, got %#v", result)
	}
}

func TestTrustModeAllowsBashButDenyRuleStillBlocks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rs, err := ParseRuleSet(nil, []string{"Bash(rm -rf *)"})
	if err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(t.TempDir(), ProfileBalanced, rs, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine.SetMode(ModeTrust)

	// Safe command: trust mode allows.
	safeArgs, _ := json.Marshal(map[string]any{"command": "echo hello"})
	result, err := engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: safeArgs},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("trust mode should allow safe command, got %#v", result)
	}

	// Dangerous command: deny rule blocks.
	badArgs, _ := json.Marshal(map[string]any{"command": "rm -rf /"})
	result, err = engine.ApproveTool(context.Background(), agentcore.ToolApprovalRequest{
		Call: agentcore.ToolCall{Name: "bash", Args: badArgs},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Approved {
		t.Fatalf("deny rule should block even in trust mode, got %#v", result)
	}
}
