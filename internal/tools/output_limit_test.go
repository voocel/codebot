package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

// bigOutputTool returns a payload past DefaultOutputLimit in the shape the tool
// is told to use, so both truncation branches get exercised.
type bigOutputTool struct {
	name       string
	structured bool
}

func (t *bigOutputTool) Name() string           { return t.name }
func (t *bigOutputTool) Description() string    { return "test" }
func (t *bigOutputTool) Schema() map[string]any { return map[string]any{} }

func (t *bigOutputTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	payload := strings.Repeat("x", DefaultOutputLimit+1)
	if t.structured {
		return json.Marshal(map[string]any{"output": payload, "exit_code": 0})
	}
	return json.Marshal(payload)
}

// limiterAt builds a limiter aimed at dirFn, the way wireSessionRuntime does.
func limiterAt(dirFn func() string) *OutputLimiter {
	l := NewOutputLimiter()
	l.SetOutputDir(dirFn)
	return l
}

// runLimiter drives the middleware the way agentcore's chain does: the limiter
// wraps tool.Execute and is handed the call it is limiting.
func runLimiter(t *testing.T, l *OutputLimiter, tool agentcore.Tool) string {
	t.Helper()
	out, err := l.Middleware()(context.Background(), agentcore.ToolCall{Name: tool.Name()}, tool.Execute)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return string(out)
}

func runLimited(t *testing.T, tool agentcore.Tool) string {
	t.Helper()
	dir := t.TempDir()
	return runLimiter(t, limiterAt(func() string { return dir }), tool)
}

// The path travels through the transcript as the tool's own JSON, so on Windows
// every separator arrives doubled. Reading it back has to yield a path that
// actually opens — that is the whole point of persisting it.
func TestPersistedOutputPathSurvivesJSONEncoding(t *testing.T) {
	for _, tc := range []struct {
		name       string
		structured bool
	}{
		{"structured", true},
		{"bare string", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := runLimited(t, &bigOutputTool{name: "bash", structured: tc.structured})
			path := PersistedOutputPath(raw)
			if path == "" {
				t.Fatalf("no path recovered from %.120s", raw)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("recovered path does not open: %v", err)
			}
		})
	}
}

// Output under the limit is never written to disk, so there is no path to hand
// back and callers must fall through to their plain cleared message.
func TestPersistedOutputPathEmptyWhenNotPersisted(t *testing.T) {
	t.Parallel()

	small, err := json.Marshal("short output")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := PersistedOutputPath(string(small)); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// A failed write leaves a placeholder where the path goes; handing that to the
// model as something it can Read is worse than admitting the content is gone.
func TestPersistedOutputPathRejectsSaveFailure(t *testing.T) {
	t.Parallel()

	text := persistedOpenTag + "\nOutput too large (99 chars). " + PersistedPathLabel + saveFailedPath + "\n\nhead\n" + persistedCloseTag
	if got := PersistedOutputPath(text); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// /new and /resume move the session directory under a running process. A
// directory captured at wiring time keeps writing into the session the process
// started in — so cleaning that session later breaks paths the live one already
// handed the model.
func TestOutputDirFollowsSessionSwitch(t *testing.T) {
	t.Parallel()

	first, second := t.TempDir(), t.TempDir()
	current := first
	limiter := limiterAt(func() string { return current })
	tool := &bigOutputTool{name: "bash", structured: true}

	before := PersistedOutputPath(runLimiter(t, limiter, tool))
	current = second
	after := PersistedOutputPath(runLimiter(t, limiter, tool))

	if !strings.HasPrefix(before, first) {
		t.Fatalf("first save landed outside %s: %s", first, before)
	}
	if !strings.HasPrefix(after, second) {
		t.Fatalf("save did not follow the session switch, still under %s: %s", first, after)
	}
}

// Outputs live in per-session directories, so the running session's own
// directory was created minutes ago. Sweeping only it would never collect
// anything — the whole point is reaching the sessions left behind.
func TestCleanOldOutputsSweepsEverySession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	stale := filepath.Join(root, "old-session", ToolOutputsSubdir)
	fresh := filepath.Join(root, "live-session", ToolOutputsSubdir)
	for _, dir := range []string{stale, fresh} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	staleFile := filepath.Join(stale, "bash-1.txt")
	freshFile := filepath.Join(fresh, "bash-2.txt")
	for _, f := range []string{staleFile, freshFile} {
		if err := os.WriteFile(f, []byte("output"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	old := time.Now().Add(-outputCleanupAge - time.Hour)
	if err := os.Chtimes(staleFile, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	CleanOldOutputs(root)

	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Fatal("stale output in another session survived the sweep")
	}
	if _, err := os.Stat(freshFile); err != nil {
		t.Fatalf("recent output was collected: %v", err)
	}
}

// Opting out is a short, deliberate list; everything else must be covered.
// A whitelist is what let MCP results through with no size handling at all.
func TestLimiterCoversEverythingExceptOptOuts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		tool    string
		limited bool
	}{
		// Persisted output is read back with read, so truncating its results
		// loops. Skill output is a procedure to follow, not data to sample.
		{"read", false},
		{"skill", false},
		{"bash", true},
		{"web_fetch", true},
		{"edit", true},
		{"mcp__github__list_issues", true},
	} {
		// Checked via the recovered path rather than the tag: json.Marshal
		// escapes "<" to "<", so the raw result never contains the tag
		// literally.
		got := runLimited(t, &bigOutputTool{name: tc.tool})
		if limited := PersistedOutputPath(got) != ""; limited != tc.limited {
			t.Errorf("%s: limited=%v, want %v", tc.tool, limited, tc.limited)
		}
	}
}
