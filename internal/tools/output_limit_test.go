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
	"github.com/voocel/litellm"
)

// bigOutput returns a result past outputLimit, as bash's JSON object when
// structured and as plain text otherwise, so both truncation branches get
// exercised.
func bigOutput(structured bool) agentcore.ToolFunc {
	return func(context.Context, agentcore.ToolCall) (agentcore.Result, error) {
		payload := strings.Repeat("x", outputLimit+1)
		if structured {
			return agentcore.JSONResult(map[string]any{"output": payload, "exit_code": 0})
		}
		return agentcore.TextResult(payload), nil
	}
}

// runLimited drives the middleware the way agentcore's chain does: the
// limiter wraps the tool's run and is handed the call it is limiting.
func runLimited(t *testing.T, name string, structured bool) string {
	t.Helper()
	l := NewOutputLimiter(t.TempDir())
	res, err := l.Middleware()(context.Background(), agentcore.ToolCall{Name: name}, bigOutput(structured))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return text(res)
}

// persistedPath returns the file a limited result points at, or "".
// Structured results carry the text under "output", the rest are plain text.
func persistedPath(text string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(text), &obj) == nil {
		text, _ = obj["output"].(string)
	}
	_, path, ok := strings.Cut(text, persistedPathLabel)
	if !ok {
		return ""
	}
	path, _, _ = strings.Cut(path, "\n")
	return strings.TrimSpace(path)
}

// A structured result carries the path inside the tool's own JSON, so on
// Windows every separator arrives doubled. Reading it back has to yield a path
// that actually opens — that is the whole point of persisting it.
func TestPersistedOutputPathSurvivesJSONEncoding(t *testing.T) {
	for _, tc := range []struct {
		name       string
		structured bool
	}{
		{"structured", true},
		{"plain text", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := runLimited(t, "bash", tc.structured)
			path := persistedPath(raw)
			if path == "" {
				t.Fatalf("no path recovered from %.120s", raw)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("recovered path does not open: %v", err)
			}
		})
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
		got := runLimited(t, tc.tool, false)
		if limited := persistedPath(got) != ""; limited != tc.limited {
			t.Errorf("%s: limited=%v, want %v", tc.tool, limited, tc.limited)
		}
	}
}

// Only a result of one text block is limited; images and the like pass
// through whole.
func TestLimiterPassesOtherResults(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", outputLimit+1)
	image := agentcore.Result{Content: []litellm.Block{litellm.Text(big), litellm.ImageBlock{Data: []byte("png"), MIME: "image/png"}}}
	res, err := NewOutputLimiter(t.TempDir()).Middleware()(context.Background(), agentcore.ToolCall{Name: "mcp__x__shot"},
		func(context.Context, agentcore.ToolCall) (agentcore.Result, error) { return image, nil })
	if err != nil || len(res.Content) != 2 || text(res) != big {
		t.Fatalf("result changed: %v, %d blocks", err, len(res.Content))
	}
}
