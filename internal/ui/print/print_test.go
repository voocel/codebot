package print

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/provider"
)

// scriptModel is a provider answering with its replies in order, then
// "done".
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

// A background command finishes after the prompt's run ended; print mode
// waits for it and for the run its result starts.
func TestRunPrintWaitsForBackgroundWork(t *testing.T) {
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
	args, _ := json.Marshal(map[string]any{"command": "sleep 0.2; echo finished", "run_in_background": true})
	model := &scriptModel{replies: []litellmtest.Reply{litellmtest.Respond(litellm.ToolUseBlock{ID: "b1", Name: "bash", Arguments: string(args)})}}
	a, err := app.Boot(app.Options{
		Cwd:      t.TempDir(),
		Mode:     interact.ModeTrust,
		UI:       UI{},
		NewModel: model.factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

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

// A path confirmed every time cannot be approved in print mode, not even in
// trust mode; the agent is told why.
func TestRunPrintRefusesWhatNeedsConfirming(t *testing.T) {
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
	args, _ := json.Marshal(map[string]any{"file_path": ".bashrc", "content": "echo hi"})
	model := &scriptModel{replies: []litellmtest.Reply{litellmtest.Respond(litellm.ToolUseBlock{ID: "w1", Name: "write", Arguments: string(args)})}}
	cwd := t.TempDir()
	a, err := app.Boot(app.Options{
		Cwd:      cwd,
		Mode:     interact.ModeTrust,
		UI:       UI{},
		NewModel: model.factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

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

// -json names each event's type and carries the text of errors, which an
// error value would lose.
func TestJSONEvents(t *testing.T) {
	for _, tt := range []struct {
		ev   agentcore.Event
		want string
	}{
		{agentcore.Retry{Attempt: 1, MaxRetries: 5, Delay: time.Second, Err: errors.New("rate limited")},
			`{"attempt":1,"delay_ms":1000,"error":"rate limited","max_retries":5,"type":"retry"}`},
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
