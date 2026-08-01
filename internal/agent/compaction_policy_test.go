package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/codebot/internal/config"
	localtools "github.com/voocel/codebot/internal/tools"
)

// persistedResultText mimics a truncated tool result as it lands in the
// transcript: the tool's own JSON, so the path arrives escaped.
func persistedResultText(t *testing.T, path string) string {
	t.Helper()
	raw, err := json.Marshal("<persisted-output>\nOutput too large (99999 chars). " +
		localtools.PersistedPathLabel + path + "\n\nhead\n</persisted-output>")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func clearOnce(t *testing.T, msgs []agentcore.AgentMessage) []agentcore.AgentMessage {
	t.Helper()
	strategy := agentctx.NewToolResultMicrocompact(agentctx.ToolResultMicrocompactConfig{
		Classifier:       CodebotToolClassifier,
		KeepRecent:       1,
		ClearedMessageFn: ClearedToolResultMessage,
	})
	out, _, err := strategy.Apply(context.Background(), nil, msgs, agentctx.Budget{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return out
}

func resultText(t *testing.T, msgs []agentcore.AgentMessage, idx int) string {
	t.Helper()
	msg, ok := msgs[idx].(agentcore.Message)
	if !ok || msg.Role != agentcore.RoleTool {
		t.Fatalf("message %d is not a tool result", idx)
	}
	return msg.Content[0].Text
}

// Oversized output is already on disk when microcompact reaches it. Replacing
// the whole block strands the file, and the model's only way back to the
// content is to re-run the command.
func TestClearedToolResultKeepsPersistedPath(t *testing.T) {
	t.Parallel()

	const path = `C:\sessions\abc\tool-outputs\bash-1.txt`
	msgs := append(
		toolExchange("a", "bash", persistedResultText(t, path)),
		toolExchange("b", "bash", "recent, protected")...,
	)

	cleared := resultText(t, clearOnce(t, msgs), 1)
	if !strings.Contains(cleared, path) {
		t.Fatalf("path dropped from cleared result: %q", cleared)
	}
	if !strings.Contains(cleared, agentctx.DefaultClearedToolResult) {
		t.Fatalf("cleared result no longer says the content is gone: %q", cleared)
	}
}

// Every pass re-clears what earlier passes cleared. If the text shifted each
// time, the prefix would be rewritten on every pass and the cache would never
// hold.
func TestClearedToolResultIsIdempotent(t *testing.T) {
	t.Parallel()

	msgs := append(
		toolExchange("a", "bash", persistedResultText(t, `C:\out\bash-1.txt`)),
		toolExchange("b", "bash", "recent, protected")...,
	)

	first := clearOnce(t, msgs)
	second := clearOnce(t, first)
	if a, b := resultText(t, first, 1), resultText(t, second, 1); a != b {
		t.Fatalf("second pass rewrote the result:\n%q\n%q", a, b)
	}
}

// Results small enough to stay in memory were never written anywhere, so there
// is no path to keep and the plain message is the honest one.
func TestClearedToolResultFallsBackWithoutPath(t *testing.T) {
	t.Parallel()

	msgs := append(
		toolExchange("a", "bash", `"a small result"`),
		toolExchange("b", "bash", "recent, protected")...,
	)

	if got := resultText(t, clearOnce(t, msgs), 1); got != agentctx.DefaultClearedToolResult {
		t.Fatalf("got %q, want the plain cleared message", got)
	}
}

func newRecoveryTestSession(t *testing.T) *Session {
	t.Helper()
	s := NewSession(SessionConfig{
		Agent:                 agentcore.NewAgent(agentcore.WithModel(&stubChatModel{})),
		Settings:              config.Resolved{MaxTurns: 10},
		Cwd:                   t.TempDir(),
		DeferredToolsPreamble: "<available-deferred-tools>\nWebSearch\n</available-deferred-tools>",
	})
	t.Cleanup(s.Close)
	return s
}

func restoredPaths(msgs []agentcore.AgentMessage) []string {
	var out []string
	for _, am := range msgs {
		msg, ok := am.(agentcore.Message)
		if !ok {
			continue
		}
		for _, b := range msg.Content {
			if b.Type != agentcore.ContentText || !strings.Contains(b.Text, "<file-restore ") {
				continue
			}
			// path is rendered with %q, which escapes Windows separators.
			rest := b.Text[strings.Index(b.Text, "path=")+len("path="):]
			quoted := rest[:strings.Index(rest, ">")]
			path, err := strconv.Unquote(quoted)
			if err != nil {
				path = quoted
			}
			out = append(out, path)
		}
	}
	return out
}

// The hook fires from both Apply (automatic, token pressure) and ForceApply
// (/compact). File restores used to hang off the explicit path only, so the
// common case recovered less — this pins them to the shared hook.
func TestPostCompactRecoveryRestoresWorkingFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	edited := filepath.Join(dir, "edited.go")
	if err := os.WriteFile(edited, []byte("package main // edited\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	s := newRecoveryTestSession(t)
	info := agentctx.SummaryInfo{ModifiedFiles: []string{edited}}

	out, err := s.postCompactRecoveryMessages(context.Background(), info, nil)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}

	paths := restoredPaths(out)
	if len(paths) != 1 || paths[0] != edited {
		t.Fatalf("restored %v, want [%s]", paths, edited)
	}
	// Bulk file content leads; the short reminders follow.
	last, _ := out[len(out)-1].(agentcore.Message)
	if !strings.Contains(last.Content[0].Text, deferredToolsTag) {
		t.Fatal("preamble must come after the file restores")
	}
}

// A file the surviving tail already carries does not need re-injecting.
func TestPostCompactRecoverySkipsFilesStillInKept(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stillThere := filepath.Join(dir, "kept.go")
	dropped := filepath.Join(dir, "dropped.go")
	for _, p := range []string{stillThere, dropped} {
		if err := os.WriteFile(p, []byte("package main\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	s := newRecoveryTestSession(t)
	kept := toolExchange("t1", "read", "file body")
	call, _ := kept[0].(agentcore.Message)
	args, err := json.Marshal(map[string]string{"path": stillThere})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	call.Content[0].ToolCall.Args = args
	kept[0] = call

	info := agentctx.SummaryInfo{ReadFiles: []string{stillThere, dropped}}
	out, err := s.postCompactRecoveryMessages(context.Background(), info, kept)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}

	paths := restoredPaths(out)
	if len(paths) != 1 || paths[0] != dropped {
		t.Fatalf("restored %v, want only the dropped file %s", paths, dropped)
	}
}

// Memory and plan files are re-injected by their own channels; restoring them
// here would duplicate that content into the window.
func TestPostCompactRecoveryExcludesSelfManagedFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mem := filepath.Join(memDir, "note.md")
	if err := os.WriteFile(mem, []byte("remembered\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := newRecoveryTestSession(t)
	out, err := s.postCompactRecoveryMessages(context.Background(),
		agentctx.SummaryInfo{ReadFiles: []string{mem}}, nil)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if paths := restoredPaths(out); len(paths) != 0 {
		t.Fatalf("restored self-managed files %v, want none", paths)
	}
}

// Only potentially blocking strategies need progress feedback.
func TestCompactionBlocksOnlyForModelBackedStages(t *testing.T) {
	t.Parallel()

	for strategy, want := range map[string]bool{
		"full_summary":             true,
		"tool_result_microcompact": false,
		"light_trim":               false,
		"some_future_stage":        true,
	} {
		if got := CompactionBlocks(strategy); got != want {
			t.Errorf("CompactionBlocks(%q) = %v, want %v", strategy, got, want)
		}
	}
}

// A model switch must update both the window and output reserve.
func TestModelSwitchResizesTheReserve(t *testing.T) {
	t.Parallel()

	m := &modelState{settings: config.Resolved{ContextWindow: 200_000, MaxOutputTokens: 8_192}}
	if _, reserve := m.applyContextWindow(200_000, 8_192); reserve != 8_192 {
		t.Fatalf("reserve = %d, want the small model's ceiling", reserve)
	}
	if _, reserve := m.applyContextWindow(200_000, 64_000); reserve != 20_000 {
		t.Fatalf("reserve = %d after switching to a large-output model, want the cap", reserve)
	}
}
