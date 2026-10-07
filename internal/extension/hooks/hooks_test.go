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
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

func TestParseMatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		match   string
		noMatch string
	}{
		{name: "exact", input: "bash", match: "Bash", noMatch: "write"},
		{name: "regex", input: "/write|edit/i", match: "Write", noMatch: "bash"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := parseMatcher(tc.input)
			if err != nil {
				t.Fatalf("parseMatcher: %v", err)
			}
			if !m.Match(tc.match) {
				t.Fatalf("expected %q to match %q", tc.match, tc.input)
			}
			if m.Match(tc.noMatch) {
				t.Fatalf("expected %q not to match %q", tc.noMatch, tc.input)
			}
		})
	}
}

// Hooks receive the result text as a JSON string and whether the call
// failed.
func TestPostToolUseMiddleware(t *testing.T) {
	t.Parallel()

	payload := filepath.Join(t.TempDir(), "payload")
	cfg := config.HooksConfig{
		"PostToolUse": {{Type: "command", Command: "cat > " + filepath.ToSlash(payload)}},
	}
	mw := newRunner(cfg, nil).PostToolUse()
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

// waitFor polls because fire-and-forget hooks give no completion signal.
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

func TestPreToolUseMiddleware(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo '{"updated_input":{"path":"/safe"}}'`, Matcher: "write"},
			{Type: "command", Command: `echo '{"block":true,"reason":"nope"}'`, Matcher: "bash", Blocking: new(true)},
		},
	}
	mw := newRunner(cfg, nil).PreToolUse()

	// The rest of the chain sees the rewritten arguments.
	var seen json.RawMessage
	res, err := mw(context.Background(), agentcore.ToolCall{Name: "write", Args: json.RawMessage(`{"path":"/raw"}`)},
		func(_ context.Context, call agentcore.ToolCall) (agentcore.Result, error) {
			seen = call.Args
			return agentcore.TextResult("ok"), nil
		})
	if err != nil || res.IsError || string(seen) != `{"path":"/safe"}` {
		t.Fatalf("result %+v, err %v, the rest saw %s", res, err, seen)
	}

	// A blocking hook stops the call before the rest of the chain.
	res, err = mw(context.Background(), agentcore.ToolCall{Name: "bash", Args: json.RawMessage(`{}`)},
		func(context.Context, agentcore.ToolCall) (agentcore.Result, error) {
			t.Fatal("the chain went on after a blocking hook")
			return agentcore.Result{}, nil
		})
	if err != nil || !res.IsError {
		t.Fatalf("expected a refusal, got %+v err=%v", res, err)
	}
}

// Other non-zero exits let the call through.
func TestPreToolUse_ExitCode2Blocks(t *testing.T) {
	t.Parallel()

	for command, blocks := range map[string]bool{
		`echo "denied" >&2; exit 2`: true,
		`echo "oops" >&2; exit 1`:   false,
	} {
		r := newRunner(config.HooksConfig{"PreToolUse": {
			{Type: "command", Command: command, Matcher: "bash", Blocking: new(true)},
		}}, nil)
		if _, err := r.preToolUse(context.Background(), "bash", json.RawMessage(`{}`)); (err != nil) != blocks {
			t.Errorf("%s: err %v, want a block %v", command, err, blocks)
		}
	}
}

func TestPromptHookUsesTheCurrentModel(t *testing.T) {
	t.Parallel()

	cfg := config.HooksConfig{
		"PreToolUse": {{Type: "prompt", Prompt: "May this run? $ARGUMENTS", Blocking: new(true)}},
	}
	current := answerModel(t, `{"ok":true}`)
	r := newRunner(cfg, func() agentcore.Model { return current })
	if _, err := r.preToolUse(context.Background(), "bash", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("first model allows: %v", err)
	}
	current = answerModel(t, `{"ok":false,"reason":"switched"}`)
	if _, err := r.preToolUse(context.Background(), "bash", json.RawMessage(`{}`)); err == nil || err.Error() != "hook: switched" {
		t.Fatalf("expected the switched model to block, got %v", err)
	}
}

func newRunner(cfg config.HooksConfig, model func() agentcore.Model) *Runner {
	r := New("test", model)
	r.Set(cfg)
	return r
}

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

// Check rejects bad events and types, missing type fields and bad matchers.
func TestCheck(t *testing.T) {
	ok := config.HookEntry{Type: "command", Command: "true", Matcher: "/^ba/"}
	if err := Check("PreToolUse", ok); err != nil {
		t.Errorf("a good hook: %v", err)
	}
	for event, he := range map[string]config.HookEntry{
		"PreToolUs":    ok,
		"Stop":         ok,
		"PostToolUse":  {Type: "commnd", Command: "true"},
		"SessionStart": {Type: "command"},
		"Notification": {Type: "http"},
		"SessionEnd":   {Type: "command", Command: "true", Matcher: "/(/"},
	} {
		if err := Check(event, he); err == nil {
			t.Errorf("%s %+v passed", event, he)
		}
	}
}

func TestHookEnv(t *testing.T) {
	r := New("sess", nil)
	r.Set(config.HooksConfig{"PreToolUse": {
		{Type: "command", Command: `test "$PLUGIN_ROOT" = /plug || exit 2`, Blocking: new(true), Env: map[string]string{"PLUGIN_ROOT": "/plug"}},
	}})
	if _, err := r.preToolUse(context.Background(), "bash", json.RawMessage(`{}`)); err != nil {
		t.Errorf("the hook went without its environment: %v", err)
	}
}

// On Windows without sh, a hook with no command_windows fails at load.
func TestCommandFor(t *testing.T) {
	he := config.HookEntry{Type: "command", Command: "./guard", CommandWindows: `& "$env:PLUGIN_ROOT\guard.ps1"`}
	for goos, want := range map[string]string{"linux": "sh ./guard", "darwin": "sh ./guard", "windows": "powershell " + he.CommandWindows} {
		if c, err := commandFor(he, goos); err != nil || c.shell[0]+" "+c.command != want {
			t.Errorf("%s: %+v %v, want %s", goos, c, err, want)
		}
	}
	he.CommandWindows = ""
	if c, err := commandFor(he, "windows"); err != nil || c.shell[0] != "sh" {
		t.Errorf("with sh on PATH: %+v %v", c, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := commandFor(he, "windows"); err == nil || !strings.Contains(err.Error(), "command_windows") {
		t.Errorf("without sh: %v", err)
	}
}

// The timeout holds even when a child process keeps the output pipe open.
func TestCommandHookTimesOut(t *testing.T) {
	t.Parallel()

	r := newRunner(config.HooksConfig{"PreToolUse": {
		{Type: "command", Command: "sleep 30; true", Blocking: new(true), Timeout: new(1)},
	}}, nil)
	start := time.Now()
	if _, err := r.preToolUse(context.Background(), "bash", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("a hook timing out blocked: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the hook took %s to time out", took)
	}
}
