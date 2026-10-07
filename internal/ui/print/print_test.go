package print

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/provider"
	"github.com/voocel/codebot/internal/interact"
)

// scriptModel answers with its replies in order, then "done".
type scriptModel struct {
	mu      sync.Mutex
	replies []litellmtest.Reply
	calls   int
}

func (m *scriptModel) Name() string { return "script" }

func (m *scriptModel) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	return litellmtest.New(m.next()).Chat(ctx, req)
}

func (m *scriptModel) Stream(ctx context.Context, req *litellm.Request) (litellm.Stream, error) {
	return litellmtest.New(m.next()).Stream(ctx, req)
}

func (m *scriptModel) next() litellmtest.Reply {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	next := litellmtest.Text("done")
	if len(m.replies) > 0 {
		next, m.replies = m.replies[0], m.replies[1:]
	}
	return next
}

func (m *scriptModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *scriptModel) factory(spec provider.ModelSpec) (agentcore.Model, error) {
	client, err := litellm.New(m)
	return agentcore.Model{Client: client, Request: litellm.Request{Model: spec.Model}}, err
}

func boot(t *testing.T, model *scriptModel, cwd string) *app.App {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := `{"provider":"anthropic","model":"claude-haiku-4-5","snapshot":false,
		"providers":{"anthropic":{"api_key":"test","models":["claude-haiku-4-5"]}}}`
	if err := os.MkdirAll(filepath.Join(home, ".codebot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codebot", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := app.Boot(app.Options{Cwd: cwd, Mode: interact.ModeTrust, UI: UI{}, NewModel: model.factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func TestRunPrintWaitsForBackgroundWork(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"command": "sleep 0.2; echo finished", "run_in_background": true})
	model := &scriptModel{replies: []litellmtest.Reply{litellmtest.Respond(litellm.ToolUseBlock{ID: "b1", Name: "bash", Arguments: string(args)})}}
	a := boot(t, model, t.TempDir())

	if err := Run(a, []string{"start it"}, true); err != nil {
		t.Fatal(err)
	}
	// The bash call, the reply after it, and the reply to its result.
	if got := model.count(); got != 3 {
		t.Fatalf("model calls = %d, want 3", got)
	}
	if n := a.Current().Tasks().Active(); n != 0 {
		t.Fatalf("%d background tasks still running", n)
	}
}

// Not even trust mode can approve it.
func TestRunPrintRefusesWhatNeedsConfirming(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"file_path": ".bashrc", "content": "echo hi"})
	model := &scriptModel{replies: []litellmtest.Reply{litellmtest.Respond(litellm.ToolUseBlock{ID: "w1", Name: "write", Arguments: string(args)})}}
	cwd := t.TempDir()
	a := boot(t, model, cwd)

	if err := Run(a, []string{"set up my shell"}, true); err != nil {
		t.Fatal(err)
	}
	var result string
	for _, m := range a.Current().History() {
		if m.Role == litellm.RoleTool {
			result = m.Text()
		}
	}
	if !strings.Contains(result, "needs confirming each time") {
		t.Fatalf("write result = %q", result)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".bashrc")); !os.IsNotExist(err) {
		t.Fatalf(".bashrc was written: %v", err)
	}
}

// The failure is reported once, by Run's caller, not also as it happens.
func TestRunPrintLeavesTheFailureToTheCaller(t *testing.T) {
	rejected := &litellm.Error{Type: litellm.ErrorTypeValidation, Message: "unknown model", Provider: "anthropic", StatusCode: 400}
	a := boot(t, &scriptModel{replies: []litellmtest.Reply{litellmtest.Fail(rejected)}}, t.TempDir())

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	err = Run(a, []string{"hi"}, false)
	os.Stderr = stderr
	w.Close()
	written, _ := io.ReadAll(r)

	if !errors.As(err, new(*litellm.Error)) {
		t.Fatalf("Run returned %v, want the provider's error", err)
	}
	if strings.Contains(string(written), "unknown model") {
		t.Fatalf("the failure was also written as it happened:\n%s", written)
	}
}

func TestJSONEvents(t *testing.T) {
	for _, tt := range []struct {
		ev   agentcore.Event
		want string
	}{
		{agentcore.Retry{Attempt: 2, Delay: time.Second, Err: errors.New("rate limited")},
			`{"attempt":2,"delay_ms":1000,"error":"rate limited","type":"retry"}`},
		{agentcore.MessageDelta{Event: litellm.TextDelta{Text: "hi"}},
			`{"delta":"hi","index":0,"kind":"text","type":"message_delta"}`},
		{agentcore.ToolUpdate{Call: agentcore.ToolCall{ID: "s1", Name: "subagent"}, Progress: subagent.Progress{Spawn: subagent.Spawn{ID: "explore#1"}, Event: agentcore.RunEnd{Reason: agentcore.EndError, Err: errors.New("down")}}},
			`{"id":"s1","name":"subagent","progress":{"agent":"explore#1","event":{"error":"down","failed_calls":0,"reason":"error","tool_calls":0,"turns":0,"type":"run_end"}},"type":"tool_update"}`},
	} {
		data, err := json.Marshal(jsonFor(tt.ev))
		if err != nil || string(data) != tt.want {
			t.Errorf("%T encoded %s, %v\nwant %s", tt.ev, data, err, tt.want)
		}
	}
	if jsonFor(agentcore.MessageDelta{Event: litellm.UsageEvent{}}) != nil {
		t.Error("a usage delta was printed")
	}
	if jsonFor(agentcore.ToolUpdate{Progress: subagent.Progress{Event: agentcore.MessageDelta{Event: litellm.UsageEvent{}}}}) != nil {
		t.Error("a sub-agent's usage delta was printed")
	}
}
