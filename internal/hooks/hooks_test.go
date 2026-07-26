package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
)

func boolPtr(b bool) *bool { return &b }

func TestParseMatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		match     string
		noMatch   string
		wantError bool
	}{
		{name: "exact", input: "bash", match: "Bash", noMatch: "write"},
		{name: "empty", input: "", match: "anything", noMatch: ""},
		{name: "regex", input: "/write|edit/i", match: "Write", noMatch: "bash"},
		{name: "invalid regex", input: "/[invalid/", wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := parseMatcher(tc.input)
			if tc.wantError {
				if err == nil {
					t.Fatal("expected parse error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMatcher: %v", err)
			}
			if !m.Match(tc.match) {
				t.Fatalf("expected %q to match %q", tc.match, tc.input)
			}
			if tc.noMatch != "" && m.Match(tc.noMatch) {
				t.Fatalf("expected %q not to match %q", tc.noMatch, tc.input)
			}
		})
	}
}

func TestNewRunner(t *testing.T) {
	t.Parallel()

	if r := New(nil, "sess1", nil, nil); r != nil {
		t.Fatal("nil config should return nil runner")
	}

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "url", Command: "echo test"},
			{Type: "command", Command: ""},
			{Type: "command", Command: "echo ok"},
		},
		"BadEvent": {
			{Type: "command", Command: "echo bad"},
		},
	}
	r := New(cfg, "sess1", nil, nil)
	if r == nil {
		t.Fatal("expected valid hook runner")
	}
	if got := len(r.hooks[PreToolUse]); got != 1 {
		t.Fatalf("expected 1 compiled hook, got %d", got)
	}
}

func TestRunPreToolUse(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo '{"block":true,"reason":"not allowed"}'`, Matcher: "bash", Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test", nil, nil)

	if _, err := r.RunPreToolUse(context.Background(), "write", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("non-matching hook should be skipped: %v", err)
	}

	_, err := r.RunPreToolUse(context.Background(), "bash", json.RawMessage(`{}`))
	if err == nil || err.Error() != "hook: not allowed" {
		t.Fatalf("expected blocking error, got %v", err)
	}
}

func TestRunPostToolUse_FireAndForget(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	cfg := config.HooksConfig{
		"PostToolUse": {
			// ToSlash: the command runs via `sh -c`, where backslashes escape.
			{Type: "command", Command: "touch " + filepath.ToSlash(marker)},
		},
	}
	r := New(cfg, "test", nil, nil)
	r.RunPostToolUse(context.Background(), "bash", nil, json.RawMessage(`"ok"`), false)

	waitFor(t, "expected PostToolUse hook to run", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
}

// waitFor polls until ok reports true, failing the test after a deadline.
// Fire-and-forget hooks give no completion signal, so tests must poll.
func waitFor(t *testing.T, desc string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(desc)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunPreToolUse_DeniedByApproval(t *testing.T) {
	t.Parallel()

	engine, err := approval.NewEngine(t.TempDir(), approval.ModeBalanced, nil, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	engine.SetApprover(func(context.Context, approval.Prompt) (approval.Choice, error) {
		return approval.ChoiceDeny, nil
	})

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: "echo ok", Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test", engine, nil)
	_, err = r.RunPreToolUse(context.Background(), "bash", json.RawMessage(`{}`))
	if err == nil || err.Error() != "hook: blocking hook command requires approval" {
		t.Fatalf("expected approval denial, got %v", err)
	}
}

func TestRunTaskCreated_Payload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outFile := filepath.Join(dir, "task-created.json")
	cfg := config.HooksConfig{
		"TaskCreated": {
			// ToSlash: the command runs via `sh -c`, where backslashes escape.
			{Type: "command", Command: "cat > " + filepath.ToSlash(outFile)},
		},
	}
	r := New(cfg, "test", nil, nil)
	task := TaskSnapshot{
		ID:          "1",
		Subject:     "Fix auth",
		Description: "Add task lifecycle hooks",
		Status:      "pending",
	}
	if err := r.RunTaskCreated(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	var payload Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Event != TaskCreated || payload.Tool != "task_create" {
		t.Fatalf("unexpected payload header: %#v", payload)
	}
	if payload.Task == nil || payload.Task.ID != "1" {
		t.Fatalf("missing task payload: %#v", payload.Task)
	}
}

func TestRunTaskCompleted_Blocking(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"TaskCompleted": {
			{Type: "command", Command: `echo '{"block":true,"reason":"verify results first"}'`, Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test", nil, nil)
	err := r.RunTaskCompleted(context.Background(),
		TaskSnapshot{ID: "1", Subject: "Fix auth", Status: "in_progress"},
		TaskSnapshot{ID: "1", Subject: "Fix auth", Status: "completed"},
	)
	if err == nil || err.Error() != "hook: verify results first" {
		t.Fatalf("expected blocking completion hook, got %v", err)
	}
}

func TestRunTaskCompleted_NonBlocking(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"TaskCompleted": {
			{Type: "command", Command: "exit 1", Blocking: boolPtr(false)},
		},
	}
	r := New(cfg, "test", nil, nil)
	err := r.RunTaskCompleted(context.Background(),
		TaskSnapshot{ID: "1", Subject: "Fix auth", Status: "in_progress"},
		TaskSnapshot{ID: "1", Subject: "Fix auth", Status: "completed"},
	)
	if err != nil {
		t.Fatalf("non-blocking completion hook should not fail: %v", err)
	}
}

func TestPreToolUse_UpdatedInput(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo '{"updated_input":{"path":"/safe"}}'`, Matcher: "write"},
		},
	}
	r := New(cfg, "test", nil, nil)

	dec, err := r.RunPreToolUse(context.Background(), "write", json.RawMessage(`{"path":"/raw"}`))
	if err != nil {
		t.Fatalf("hook should not block: %v", err)
	}
	if string(dec.UpdatedInput) != `{"path":"/safe"}` {
		t.Fatalf("expected rewritten input, got %q", string(dec.UpdatedInput))
	}
}

func TestWrapGate(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo '{"updated_input":{"path":"/safe"}}'`, Matcher: "write"},
			{Type: "command", Command: `echo '{"block":true,"reason":"nope"}'`, Matcher: "bash", Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test", nil, nil)

	t.Run("hook rewrite reaches next gate and kernel", func(t *testing.T) {
		var seen json.RawMessage
		gate := r.WrapGate(func(_ context.Context, req agentcore.GateRequest) (*agentcore.GateDecision, error) {
			seen = req.Call.Args
			return &agentcore.GateDecision{Allowed: true}, nil
		})
		dec, err := gate(context.Background(), agentcore.GateRequest{
			Call: agentcore.ToolCall{Name: "write", Args: json.RawMessage(`{"path":"/raw"}`)},
		})
		if err != nil || dec == nil || !dec.Allowed {
			t.Fatalf("expected allow, got %+v err=%v", dec, err)
		}
		if string(seen) != `{"path":"/safe"}` {
			t.Fatalf("permission gate should see hook-updated args, got %s", seen)
		}
		if string(dec.UpdatedArgs) != `{"path":"/safe"}` {
			t.Fatalf("kernel should receive the rewrite, got %s", dec.UpdatedArgs)
		}
	})

	t.Run("blocking hook denies without reaching next gate", func(t *testing.T) {
		gate := r.WrapGate(func(_ context.Context, _ agentcore.GateRequest) (*agentcore.GateDecision, error) {
			t.Fatal("next gate must not run after a blocking hook")
			return nil, nil
		})
		dec, err := gate(context.Background(), agentcore.GateRequest{
			Call: agentcore.ToolCall{Name: "bash", Args: json.RawMessage(`{}`)},
		})
		if err != nil || dec == nil || dec.Allowed {
			t.Fatalf("expected deny, got %+v err=%v", dec, err)
		}
	})

	t.Run("next gate rewrite wins over hook rewrite", func(t *testing.T) {
		gate := r.WrapGate(func(_ context.Context, _ agentcore.GateRequest) (*agentcore.GateDecision, error) {
			return &agentcore.GateDecision{Allowed: true, UpdatedArgs: json.RawMessage(`{"path":"/final"}`)}, nil
		})
		dec, err := gate(context.Background(), agentcore.GateRequest{
			Call: agentcore.ToolCall{Name: "write", Args: json.RawMessage(`{"path":"/raw"}`)},
		})
		if err != nil || dec == nil {
			t.Fatalf("unexpected: %+v err=%v", dec, err)
		}
		if string(dec.UpdatedArgs) != `{"path":"/final"}` {
			t.Fatalf("permission gate rewrite must win, got %s", dec.UpdatedArgs)
		}
	})
}

func TestPreToolUse_ExitCode2Blocks(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo "denied" >&2; exit 2`, Matcher: "bash", Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test", nil, nil)

	if _, err := r.RunPreToolUse(context.Background(), "bash", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected exit-2 hook to block")
	}
}

func TestPreToolUse_ExitCode1NonBlocking(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo "oops" >&2; exit 1`, Matcher: "bash", Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test", nil, nil)

	if _, err := r.RunPreToolUse(context.Background(), "bash", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("exit-1 should be a non-blocking error, got block: %v", err)
	}
}

func TestUserPromptSubmit_AdditionalContext(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"UserPromptSubmit": {
			{Type: "command", Command: `echo '{"additional_context":"remember: be concise"}'`},
		},
	}
	r := New(cfg, "test", nil, nil)

	dec, err := r.RunUserPromptSubmit(context.Background(), "hello")
	if err != nil {
		t.Fatalf("hook should not block: %v", err)
	}
	if dec.AdditionalContext != "remember: be concise" {
		t.Fatalf("expected additional context, got %q", dec.AdditionalContext)
	}
}

func TestRunSubagentStop_FireAndForget(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := filepath.Join(dir, "stop.json")
	cfg := config.HooksConfig{
		"SubagentStop": {
			// ToSlash: the command runs via `sh -c`, where backslashes escape.
			{Type: "command", Command: "cat > " + filepath.ToSlash(marker), Matcher: "researcher"},
		},
	}
	r := New(cfg, "test", nil, nil)
	r.RunSubagentStop(context.Background(), "researcher")

	var data []byte
	waitFor(t, "expected SubagentStop hook to run", func() bool {
		b, err := os.ReadFile(marker)
		if err != nil || len(b) == 0 {
			return false
		}
		data = b
		return true
	})
	var payload Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Event != SubagentStop || payload.Agent != "researcher" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestWrapGateNilDecisionKeepsRewrite(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo '{"updated_input":{"path":"/safe"}}'`, Matcher: "write"},
		},
	}
	r := New(cfg, "test", nil, nil)

	gate := r.WrapGate(func(_ context.Context, _ agentcore.GateRequest) (*agentcore.GateDecision, error) {
		return nil, nil // no opinion
	})
	dec, err := gate(context.Background(), agentcore.GateRequest{
		Call: agentcore.ToolCall{Name: "write", Args: json.RawMessage(`{"path":"/raw"}`)},
	})
	if err != nil || dec == nil || !dec.Allowed {
		t.Fatalf("expected synthesized allow, got %+v err=%v", dec, err)
	}
	if string(dec.UpdatedArgs) != `{"path":"/safe"}` {
		t.Fatalf("hook rewrite must survive a nil next decision, got %s", dec.UpdatedArgs)
	}
}
