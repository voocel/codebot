package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

const (
	outputLimit      = 30 * 1024 // bytes kept whole in the transcript
	outputCleanupAge = 7 * 24 * time.Hour

	ToolOutputsSubdir = "tool-outputs"

	persistedPathLabel = "Full output saved to: "
	persistedOpenTag   = "<persisted-output>"
	persistedCloseTag  = "</persisted-output>"
	saveFailedPath     = "(save failed)"
)

// unlimitedTools is an opt-out list, so tools registered at runtime, such as
// MCP tools, are limited by default.
//
//   - read: persisted output is read back with it, so truncating would loop.
//   - skill: its output is a procedure to follow, not data to sample.
var unlimitedTools = map[string]struct{}{
	"read":  {},
	"skill": {},
}

// OutputLimiter saves oversized tool output to disk and leaves a head/tail
// preview with the path. As middleware it covers every tool, MCP included.
type OutputLimiter struct {
	dir string
}

func NewOutputLimiter(dir string) *OutputLimiter {
	return &OutputLimiter{dir: dir}
}

// Middleware must be installed innermost (last in the slice) so hooks and
// telemetry see the same shortened result as the model.
func (l *OutputLimiter) Middleware() agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolFunc) (agentcore.Result, error) {
		res, err := next(ctx, call)
		if err != nil {
			return res, err
		}
		if _, optedOut := unlimitedTools[call.Name]; optedOut || len(res.Content) != 1 {
			return res, nil
		}
		text, ok := res.Content[0].(litellm.TextBlock)
		if !ok || len(text.Text) <= outputLimit {
			return res, nil
		}
		text.Text = l.truncateAndSave(call.Name, text.Text)
		res.Content = []litellm.Block{text}
		return res, nil
	}
}

func (l *OutputLimiter) truncateAndSave(toolName, result string) string {
	return truncatedOutputSummary(result, l.saveToFile(toolName, result))
}

func (l *OutputLimiter) saveToFile(toolName, text string) string {
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return saveFailedPath
	}
	filename := fmt.Sprintf("%s-%d.txt", toolName, time.Now().UnixMilli())
	path := filepath.Join(l.dir, filename)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return saveFailedPath
	}
	return path
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
			persistedOpenTag, total, persistedPathLabel, path, head, omitted, tail, persistedCloseTag,
		)
	}
	return fmt.Sprintf(
		"%s\nOutput too large (%d chars). %s%s\n\n%s\n\n[%d characters omitted]\n%s",
		persistedOpenTag, total, persistedPathLabel, path, head, omitted, persistedCloseTag,
	)
}

// CleanOldOutputs sweeps every session, not just the live one, whose
// directory is too new to hold anything old enough to delete.
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
