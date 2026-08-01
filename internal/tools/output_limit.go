package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// fallbackOutputDir catches a limiter that was never pointed at a session —
// tests and any wiring path that forgot SetOutputDir. Writing somewhere
// harmless beats failing the tool call.
func fallbackOutputDir() string {
	return filepath.Join(os.TempDir(), "codebot-outputs")
}

// unlimitedTools opt out of truncation; everything else is limited, MCP
// included. A whitelist would need extending per tool and silently misses the
// ones registered at runtime — that is how MCP results went unhandled.
//
//   - read: persisted output is read back with it, so truncating loops.
//   - skill: its output is a procedure to follow, not data to sample, and the
//     model has no reason to suspect a preview is incomplete. opencode
//     protects it for the same reason (PRUNE_PROTECTED_TOOLS).
var unlimitedTools = map[string]struct{}{
	"read":  {},
	"skill": {},
}

// OutputLimiter truncates oversized tool output to disk, leaving a head/tail
// preview and a path in the transcript.
//
// Middleware, not a Tool decorator, and that is the whole design: substituting
// the Tool object upcasts it to bare agentcore.Tool and silently drops every
// optional capability it implements — Validator, Previewer, ConcurrencySafe,
// PermissionMetadata — plus whichever one gets added next. Wrapping the
// execution leaves the tool's identity intact.
type OutputLimiter struct {
	mu    sync.Mutex
	dirFn func() string
	limit int
}

func NewOutputLimiter() *OutputLimiter {
	return &OutputLimiter{limit: DefaultOutputLimit}
}

// SetOutputDir points the limiter at the live session's output directory.
// dirFn is called per save, not once: /new and /resume move the directory
// underneath a running process, and a path captured at wiring time keeps
// writing into a session that later gets swept.
func (l *OutputLimiter) SetOutputDir(dirFn func() string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dirFn = dirFn
}

// Middleware returns the hook to register with agentcore. Install it innermost
// (last in the middleware slice) so hooks and telemetry observe the same
// shortened result the model will see.
func (l *OutputLimiter) Middleware() agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolExecuteFunc) (json.RawMessage, error) {
		result, err := next(ctx, call.Args)
		if err != nil {
			return result, err
		}
		if _, optedOut := unlimitedTools[call.Name]; optedOut {
			return result, nil
		}
		if len(result) <= l.limit {
			return result, nil
		}
		return l.truncateAndSave(call.Name, result), nil
	}
}

func (l *OutputLimiter) outputDir() string {
	l.mu.Lock()
	dirFn := l.dirFn
	l.mu.Unlock()
	if dirFn != nil {
		if dir := dirFn(); dir != "" {
			return dir
		}
	}
	return fallbackOutputDir()
}

func (l *OutputLimiter) truncateAndSave(toolName string, result json.RawMessage) json.RawMessage {
	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err == nil {
		if text, ok := obj["output"].(string); ok {
			path := l.saveToFile(toolName, text)
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

	path := l.saveToFile(toolName, text)
	return buildTruncatedOutput(text, path)
}

func (l *OutputLimiter) saveToFile(toolName, text string) string {
	dir := l.outputDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return saveFailedPath
	}
	filename := fmt.Sprintf("%s-%d.txt", toolName, time.Now().UnixMilli())
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
