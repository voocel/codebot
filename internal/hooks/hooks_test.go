package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
			{Type: "command", Command: `echo '{"blocked":true,"reason":"not allowed"}'`, Matcher: "bash", Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test", nil, nil)

	if err := r.RunPreToolUse(context.Background(), "write", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("non-matching hook should be skipped: %v", err)
	}

	err := r.RunPreToolUse(context.Background(), "bash", json.RawMessage(`{}`))
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
			{Type: "command", Command: "touch " + marker},
		},
	}
	r := New(cfg, "test", nil, nil)
	r.RunPostToolUse(context.Background(), "bash", nil, json.RawMessage(`"ok"`), false)

	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected PostToolUse hook to run: %v", err)
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
	err = r.RunPreToolUse(context.Background(), "bash", json.RawMessage(`{}`))
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
			{Type: "command", Command: "cat > " + outFile},
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
			{Type: "command", Command: `echo '{"blocked":true,"reason":"verify results first"}'`, Blocking: boolPtr(true)},
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
