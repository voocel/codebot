package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/codebot/internal/config"
)

func TestParseMatcher_Exact(t *testing.T) {
	m, err := parseMatcher("bash")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("bash") {
		t.Error("should match exact name")
	}
	if !m.Match("Bash") {
		t.Error("should match case-insensitively")
	}
	if m.Match("write") {
		t.Error("should not match different name")
	}
}

func TestParseMatcher_Empty(t *testing.T) {
	m, err := parseMatcher("")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("anything") {
		t.Error("empty matcher should match all")
	}
}

func TestParseMatcher_Regex(t *testing.T) {
	m, err := parseMatcher("/write|edit/")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("write") {
		t.Error("should match write")
	}
	if !m.Match("edit") {
		t.Error("should match edit")
	}
	if m.Match("bash") {
		t.Error("should not match bash")
	}
}

func TestParseMatcher_RegexCaseInsensitive(t *testing.T) {
	m, err := parseMatcher("/write/i")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("Write") {
		t.Error("should match Write with /i flag")
	}
}

func TestParseMatcher_InvalidRegex(t *testing.T) {
	_, err := parseMatcher("/[invalid/")
	if err == nil {
		t.Error("should return error for invalid regex")
	}
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

func TestNew_NoHooks(t *testing.T) {
	r := New(nil, "sess1")
	if r != nil {
		t.Error("nil config should return nil runner")
	}

	r = New(config.HooksConfig{}, "sess1")
	if r != nil {
		t.Error("empty config should return nil runner")
	}
}

func TestNew_SkipsInvalid(t *testing.T) {
	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "url", Command: "echo test"},       // wrong type
			{Type: "command", Command: ""},              // empty command
			{Type: "command", Command: "echo ok"},       // valid
		},
		"BadEvent": {
			{Type: "command", Command: "echo bad"},      // unknown event
		},
	}
	r := New(cfg, "sess1")
	if r == nil {
		t.Fatal("should have one valid hook")
	}
	if len(r.hooks[PreToolUse]) != 1 {
		t.Errorf("expected 1 PreToolUse hook, got %d", len(r.hooks[PreToolUse]))
	}
}

func TestRunPreToolUse_Blocking_ExitError(t *testing.T) {
	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: "exit 1", Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test")
	err := r.RunPreToolUse(context.Background(), "bash", json.RawMessage(`{}`))
	if err == nil {
		t.Error("blocking hook with exit 1 should return error")
	}
}

func TestRunPreToolUse_Blocking_JSONBlocked(t *testing.T) {
	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo '{"blocked":true,"reason":"not allowed"}'`, Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test")
	err := r.RunPreToolUse(context.Background(), "bash", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("should block")
	}
	if err.Error() != "hook: not allowed" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunPreToolUse_Blocking_JSONBlockedOnSuccess(t *testing.T) {
	// Even exit 0, if stdout says blocked, it should block.
	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo '{"blocked":true}'`, Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test")
	err := r.RunPreToolUse(context.Background(), "bash", json.RawMessage(`{}`))
	if err == nil {
		t.Error("should block even on exit 0 when stdout says blocked")
	}
}

func TestRunPreToolUse_NonBlocking(t *testing.T) {
	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: "exit 1", Blocking: boolPtr(false)},
		},
	}
	r := New(cfg, "test")
	err := r.RunPreToolUse(context.Background(), "bash", json.RawMessage(`{}`))
	if err != nil {
		t.Errorf("non-blocking hook error should not propagate: %v", err)
	}
}

func TestRunPreToolUse_MatcherFilters(t *testing.T) {
	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: "exit 1", Matcher: "write", Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test")
	// Should not match "bash".
	if err := r.RunPreToolUse(context.Background(), "bash", nil); err != nil {
		t.Errorf("should not run for non-matching tool: %v", err)
	}
	// Should match "write".
	if err := r.RunPreToolUse(context.Background(), "write", nil); err == nil {
		t.Error("should run and block for matching tool")
	}
}

func TestRunPostToolUse_FireAndForget(t *testing.T) {
	// PostToolUse writes a marker file to prove it ran.
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	cfg := config.HooksConfig{
		"PostToolUse": {
			{Type: "command", Command: "touch " + marker},
		},
	}
	r := New(cfg, "test")
	r.RunPostToolUse(context.Background(), "bash", nil, json.RawMessage(`"ok"`), false)

	// Wait briefly for goroutine.
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err != nil {
		t.Error("PostToolUse hook should have created marker file")
	}
}

func TestExecCommand_Timeout(t *testing.T) {
	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: "sleep 10", Blocking: boolPtr(true), Timeout: intPtr(1)},
		},
	}
	r := New(cfg, "test")
	start := time.Now()
	err := r.RunPreToolUse(context.Background(), "bash", nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Error("should timeout")
	}
	if elapsed > 5*time.Second {
		t.Errorf("took too long: %v", elapsed)
	}
}

func TestExecCommand_EnvVars(t *testing.T) {
	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: `echo "$HOOK_EVENT|$HOOK_TOOL_NAME|$HOOK_SESSION_ID"`, Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "sess-42")
	// Won't block (exit 0, non-JSON stdout).
	err := r.RunPreToolUse(context.Background(), "bash", nil)
	if err != nil {
		t.Errorf("should succeed: %v", err)
	}
}

func TestExecCommand_StdinPayload(t *testing.T) {
	// Hook reads stdin and writes it to a file.
	dir := t.TempDir()
	outFile := filepath.Join(dir, "payload.json")
	cfg := config.HooksConfig{
		"PreToolUse": {
			{Type: "command", Command: "cat > " + outFile, Blocking: boolPtr(true)},
		},
	}
	r := New(cfg, "test")
	args := json.RawMessage(`{"command":"ls"}`)
	if err := r.RunPreToolUse(context.Background(), "bash", args); err != nil {
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
	if payload.Event != PreToolUse {
		t.Errorf("event = %q, want PreToolUse", payload.Event)
	}
	if payload.Tool != "bash" {
		t.Errorf("tool = %q, want bash", payload.Tool)
	}
}
