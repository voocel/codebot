package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/voocel/agentcore"
)

const (
	// DefaultOutputLimit is the maximum tool output size before truncation (30KB).
	DefaultOutputLimit = 30 * 1024
	outputHead         = 1000 // characters to keep from the start
	outputTail         = 1000 // characters to keep from the end
	outputSubDir       = "codebot-outputs"
	outputCleanupAge   = 7 * 24 * time.Hour
)

// outputDir returns the directory for storing truncated tool output.
func outputDir() string {
	return filepath.Join(os.TempDir(), outputSubDir)
}

// limitableTools is the set of tools whose output should be size-limited.
var limitableTools = map[string]struct{}{
	"read":       {},
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
	dir := outputDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "(save failed)"
	}
	filename := fmt.Sprintf("%s-%d.txt", t.inner.Name(), time.Now().UnixMilli())
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "(save failed)"
	}
	return path
}

func buildTruncatedOutput(text, path string) json.RawMessage {
	data, _ := json.Marshal(truncatedOutputSummary(text, path))
	return data
}

func truncatedOutputSummary(text, path string) string {
	runes := []rune(text)
	headEnd := min(outputHead, len(runes))
	head := string(runes[:headEnd])

	var tail string
	if len(runes) > outputHead+outputTail {
		tail = string(runes[len(runes)-outputTail:])
	}

	omitted := len(runes) - headEnd
	if tail != "" {
		omitted -= outputTail
	}

	summary := fmt.Sprintf("%s\n\n[Output saved to %s] [%d characters omitted]\n\n%s",
		head, path, omitted, tail)
	return summary
}

// CleanOldOutputs removes tool output files older than 7 days.
func CleanOldOutputs() {
	dir := outputDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-outputCleanupAge)
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
