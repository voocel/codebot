package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent/permission"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
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
	r := newRunner(t, cfg, nil)
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
	r := newRunner(t, cfg, nil)

	if _, err := r.preToolUse(context.Background(), "write", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("non-matching hook should be skipped: %v", err)
	}

	_, err := r.preToolUse(context.Background(), "bash", json.RawMessage(`{}`))
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
	r := newRunner(t, cfg, nil)
	r.postToolUse("bash", nil, json.RawMessage(`"ok"`), false)

	waitFor(t, "expected PostToolUse hook to run", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
}

// The PostToolUse hooks get the result's text as a JSON string, and whether
// the call failed.
func TestPostToolUseMiddleware(t *testing.T) {
	t.Parallel()

	payload := filepath.Join(t.TempDir(), "payload")
	cfg := config.HooksConfig{
		"PostToolUse": {{Type: "command", Command: "cat > " + filepath.ToSlash(payload)}},
	}
	mw := newRunner(t, cfg, nil).PostToolUse()
	res, err := mw(context.Background(), agentcore.ToolCall{Name: "bash", Args: json.RawMessage(`{"command":"false"}`)},
		func(context.Context, agentcore.ToolCall) (agentcore.Result, error) {
			return agentcore.ErrorResult("exit 1"), nil
		})
	if err != nil || !res.IsError {
		t.Fatalf("the result changed: %+v, %v", res, err)
	}
	var got struct {
		Output  string `json:"output"`
		IsError bool   `json:"is_error"`
	}
	waitFor(t, "expected PostToolUse hook to run", func() bool {
		data, err := os.ReadFile(payload)
		return err == nil && json.Unmarshal(data, &got) == nil
	})
	if got.Output != "exit 1" || !got.IsError {
		t.Fatalf("payload = %+v", got)
	}
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

	// A denial stores nothing, so the user's approvals file is only read.
	engine, err := permission.NewEngine(permission.Config{
		Cwd:  t.TempDir(),
		Mode: interact.ModeBalanced,
		UI: approveFunc(func(context.Context, interact.Approval) (interact.Verdict, error) {
			return interact.Verdict{Choice: interact.Deny}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: "echo ok", Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test", engine, nil)
	_, err = r.preToolUse(context.Background(), "bash", json.RawMessage(`{}`))
	if err == nil || !strings.HasPrefix(err.Error(), "hook: The user denied this") {
		t.Fatalf("expected approval denial, got %v", err)
	}
}

func TestPreToolUse_UpdatedInput(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo '{"updated_input":{"path":"/safe"}}'`, Matcher: "write"},
		},
	}
	r := newRunner(t, cfg, nil)

	dec, err := r.preToolUse(context.Background(), "write", json.RawMessage(`{"path":"/raw"}`))
	if err != nil {
		t.Fatalf("hook should not block: %v", err)
	}
	if string(dec.UpdatedInput) != `{"path":"/safe"}` {
		t.Fatalf("expected rewritten input, got %q", string(dec.UpdatedInput))
	}
}

func TestPreToolUseMiddleware(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo '{"updated_input":{"path":"/safe"}}'`, Matcher: "write"},
			{Type: "command", Command: `echo '{"block":true,"reason":"nope"}'`, Matcher: "bash", Blocking: boolPtr(true)},
		},
	}
	mw := newRunner(t, cfg, nil).PreToolUse()

	// The rewrite is what the rest of the chain decides on and runs.
	var seen json.RawMessage
	res, err := mw(context.Background(), agentcore.ToolCall{Name: "write", Args: json.RawMessage(`{"path":"/raw"}`)},
		func(_ context.Context, call agentcore.ToolCall) (agentcore.Result, error) {
			seen = call.Args
			return agentcore.TextResult("ok"), nil
		})
	if err != nil || res.IsError || string(seen) != `{"path":"/safe"}` {
		t.Fatalf("result %+v, err %v, the rest saw %s", res, err, seen)
	}

	// A blocking hook refuses the call before the rest of the chain.
	res, err = mw(context.Background(), agentcore.ToolCall{Name: "bash", Args: json.RawMessage(`{}`)},
		func(context.Context, agentcore.ToolCall) (agentcore.Result, error) {
			t.Fatal("the chain went on after a blocking hook")
			return agentcore.Result{}, nil
		})
	if err != nil || !res.IsError {
		t.Fatalf("expected a refusal, got %+v err=%v", res, err)
	}
}

func TestPreToolUse_ExitCode2Blocks(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo "denied" >&2; exit 2`, Matcher: "bash", Blocking: boolPtr(true)},
		},
	}
	r := newRunner(t, cfg, nil)

	if _, err := r.preToolUse(context.Background(), "bash", json.RawMessage(`{}`)); err == nil {
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
	r := newRunner(t, cfg, nil)

	if _, err := r.preToolUse(context.Background(), "bash", json.RawMessage(`{}`)); err != nil {
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
	r := newRunner(t, cfg, nil)

	dec, err := r.RunUserPromptSubmit(context.Background(), "hello")
	if err != nil {
		t.Fatalf("hook should not block: %v", err)
	}
	if dec.AdditionalContext != "remember: be concise" {
		t.Fatalf("expected additional context, got %q", dec.AdditionalContext)
	}
}

// A prompt hook asks the model the conversation has switched to, not the one
// it started with.
func TestPromptHookUsesTheCurrentModel(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {{Type: "prompt", Prompt: "May this run? $ARGUMENTS", Blocking: boolPtr(true)}},
	}
	current := answerModel(t, `{"ok":true}`)
	r := newRunner(t, cfg, func() agentcore.Model { return current })
	if _, err := r.preToolUse(context.Background(), "bash", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("first model allows: %v", err)
	}
	current = answerModel(t, `{"ok":false,"reason":"switched"}`)
	if _, err := r.preToolUse(context.Background(), "bash", json.RawMessage(`{}`)); err == nil || err.Error() != "hook: switched" {
		t.Fatalf("expected the switched model to block, got %v", err)
	}
}

// newRunner compiles cfg with an engine that lets every hook run.
func newRunner(t *testing.T, cfg config.HooksConfig, model func() agentcore.Model) *Runner {
	t.Helper()
	engine, err := permission.NewEngine(permission.Config{Cwd: t.TempDir(), Mode: interact.ModeTrust})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return New(cfg, "test", engine, model)
}

// answerModel is a model that always answers with text.
func answerModel(t *testing.T, text string) agentcore.Model {
	t.Helper()
	replies := make([]litellmtest.Reply, 4)
	for i := range replies {
		replies[i] = litellmtest.Text(text)
	}
	client, err := litellm.New(litellmtest.New(replies...))
	if err != nil {
		t.Fatal(err)
	}
	return agentcore.Model{Client: client, Request: litellm.Request{Model: "m"}}
}

// approveFunc is a UI that answers approvals with itself.
type approveFunc func(context.Context, interact.Approval) (interact.Verdict, error)

func (f approveFunc) Approve(ctx context.Context, a interact.Approval) (interact.Verdict, error) {
	return f(ctx, a)
}

func (approveFunc) Ask(context.Context, []interact.Question) (interact.Answers, error) {
	return interact.Answers{}, interact.ErrUnsupported
}
