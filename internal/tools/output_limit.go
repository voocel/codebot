package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/agentcore"
)

const (
	// DefaultOutputLimit is the maximum tool output size before truncation (30KB).
	DefaultOutputLimit = 30 * 1024
	outputCleanupAge   = 7 * 24 * time.Hour

	// ToolOutputsSubdir names the per-session directory holding output too
	// large to keep in the transcript. Lives here rather than in config
	// because this package is what writes into it.
	ToolOutputsSubdir = "tool-outputs"

	// PersistedPathLabel introduces the file a truncated result was saved to.
	// The line is the model's only route back to the full output, so it has to
	// survive microcompact clearing it — see PersistedOutputPath.
	PersistedPathLabel = "Full output saved to: "
	persistedOpenTag   = "<persisted-output>"
	persistedCloseTag  = "</persisted-output>"
	saveFailedPath     = "(save failed)"
)

// fallbackOutputDir catches tools that were never pointed at a session — tests
// and any wiring path that forgot SetOutputDir. Writing somewhere harmless
// beats failing the tool call.
func fallbackOutputDir() string {
	return filepath.Join(os.TempDir(), "codebot-outputs")
}

// limitableTools is the set of tools whose output should be size-limited.
// read is excluded: persisted output files are read via Read tool, and
// truncating Read results would cause an infinite loop (Read → persist → Read → persist).
var limitableTools = map[string]struct{}{
	"bash":       {},
	"grep":       {},
	"glob":       {},
	"ls":         {},
	"web_fetch":  {},
	"web_search": {},
}

// OutputLimitedTool wraps a Tool and truncates oversized output to disk.
type OutputLimitedTool struct {
	inner agentcore.Tool
	limit int
	dirFn func() string
}

// WrapWithOutputLimit wraps tools in the limitable set with output truncation.
// Tools not in the set are returned unchanged.
func WrapWithOutputLimit(tools []agentcore.Tool) []agentcore.Tool {
	out := make([]agentcore.Tool, len(tools))
	for i, t := range tools {
		if _, ok := limitableTools[t.Name()]; ok {
			out[i] = &OutputLimitedTool{inner: t, limit: DefaultOutputLimit}
		} else {
			out[i] = t
		}
	}
	return out
}

// SetOutputDir points the wrapped tools at their session's output directory.
//
// dirFn is called per save, not once: /new and /resume move the session
// directory underneath a running process. A path captured at wiring time keeps
// writing into the session the process started in, so cleaning that session
// later breaks paths the live one already handed the model.
func SetOutputDir(tools []agentcore.Tool, dirFn func() string) {
	for _, t := range tools {
		if lt, ok := t.(*OutputLimitedTool); ok {
			lt.dirFn = dirFn
		}
	}
}

func (t *OutputLimitedTool) outputDir() string {
	if t.dirFn != nil {
		if dir := t.dirFn(); dir != "" {
			return dir
		}
	}
	return fallbackOutputDir()
}

// Unwrap returns the underlying tool, allowing type assertions through the wrapper.
func (t *OutputLimitedTool) Unwrap() agentcore.Tool { return t.inner }

func (t *OutputLimitedTool) Name() string           { return t.inner.Name() }
func (t *OutputLimitedTool) Description() string    { return t.inner.Description() }
func (t *OutputLimitedTool) Schema() map[string]any { return t.inner.Schema() }

// Label forwards the optional ToolLabeler interface.
func (t *OutputLimitedTool) Label() string {
	if labeler, ok := t.inner.(agentcore.ToolLabeler); ok {
		return labeler.Label()
	}
	return t.inner.Name()
}

func (t *OutputLimitedTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	result, err := t.inner.Execute(ctx, args)
	if err != nil {
		return result, err
	}
	if len(result) <= t.limit {
		return result, nil
	}
	return t.truncateAndSave(result), nil
}

func (t *OutputLimitedTool) truncateAndSave(result json.RawMessage) json.RawMessage {
	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err == nil {
		if text, ok := obj["output"].(string); ok {
			path := t.saveToFile(text)
			obj["output"] = truncatedOutputSummary(text, path)
			data, merr := json.Marshal(obj)
			if merr == nil {
				return data
			}
		}
	}

	// Unwrap JSON string to get raw text; fall back to raw bytes.
	var text string
	if err := json.Unmarshal(result, &text); err != nil {
		text = string(result)
	}

	path := t.saveToFile(text)
	return buildTruncatedOutput(text, path)
}

func (t *OutputLimitedTool) saveToFile(text string) string {
	dir := t.outputDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return saveFailedPath
	}
	filename := fmt.Sprintf("%s-%d.txt", t.inner.Name(), time.Now().UnixMilli())
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return saveFailedPath
	}
	return path
}

func buildTruncatedOutput(text, path string) json.RawMessage {
	data, _ := json.Marshal(truncatedOutputSummary(text, path))
	return data
}

func truncatedOutputSummary(text, path string) string {
	runes := []rune(text)
	total := len(runes)

	const headSize = 1500
	const tailSize = 500

	headEnd := min(headSize, total)
	head := string(runes[:headEnd])

	var tail string
	if total > headSize+tailSize {
		tail = string(runes[total-tailSize:])
	}

	omitted := total - headEnd
	if tail != "" {
		omitted -= tailSize
	}

	if tail != "" {
		return fmt.Sprintf(
			"%s\nOutput too large (%d chars). %s%s\n\n%s\n\n[%d characters omitted]\n\n%s\n%s",
			persistedOpenTag, total, PersistedPathLabel, path, head, omitted, tail, persistedCloseTag,
		)
	}
	return fmt.Sprintf(
		"%s\nOutput too large (%d chars). %s%s\n\n%s\n\n[%d characters omitted]\n%s",
		persistedOpenTag, total, PersistedPathLabel, path, head, omitted, persistedCloseTag,
	)
}

// PersistedOutputPath returns the file a truncated tool result was saved to, or
// "" when there is none to point at.
//
// Tool output reaches the transcript as the tool's own JSON, so the path lands
// escaped — on Windows every separator is doubled. Decoding first is what makes
// the path usable rather than a mangled string.
//
// It also accepts its own shortened output, which is plain text by then. That
// is what keeps microcompact idempotent across passes.
func PersistedOutputPath(text string) string {
	decoded := decodeToolOutput(text)
	i := strings.Index(decoded, PersistedPathLabel)
	if i < 0 {
		return ""
	}
	path := decoded[i+len(PersistedPathLabel):]
	if end := strings.IndexAny(path, "\r\n"); end >= 0 {
		path = path[:end]
	}
	path = strings.TrimSpace(path)
	if path == saveFailedPath {
		return ""
	}
	return path
}

// decodeToolOutput unwraps the JSON a tool returned. Structured results carry
// the text under "output" (bash and friends); the rest are bare JSON strings.
// Anything else is already plain text and passes through.
func decodeToolOutput(text string) string {
	var s string
	if json.Unmarshal([]byte(text), &s) == nil {
		return s
	}
	var obj map[string]any
	if json.Unmarshal([]byte(text), &obj) == nil {
		if out, ok := obj["output"].(string); ok {
			return out
		}
	}
	return text
}

// CleanOldOutputs removes tool output files older than 7 days from every
// session under sessionsRoot.
//
// It sweeps all sessions rather than the live one because outputs are stored
// per session: the running session's own directory was created minutes ago and
// can never hold anything old enough to collect.
func CleanOldOutputs(sessionsRoot string) {
	sessions, err := os.ReadDir(sessionsRoot)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-outputCleanupAge)
	for _, s := range sessions {
		if !s.IsDir() {
			continue
		}
		cleanOutputDir(filepath.Join(sessionsRoot, s.Name(), ToolOutputsSubdir), cutoff)
	}
}

func cleanOutputDir(dir string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
