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
	// outputLimit is the largest tool output kept whole in the transcript.
	outputLimit      = 30 * 1024
	outputCleanupAge = 7 * 24 * time.Hour

	// ToolOutputsSubdir names the per-session directory holding output too
	// large to keep in the transcript. Lives here rather than in config
	// because this package is what writes into it.
	ToolOutputsSubdir = "tool-outputs"

	// persistedPathLabel introduces the file a truncated result was saved to.
	persistedPathLabel = "Full output saved to: "
	persistedOpenTag   = "<persisted-output>"
	persistedCloseTag  = "</persisted-output>"
	saveFailedPath     = "(save failed)"
)

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
// It is middleware rather than part of each tool so that it covers every
// tool, MCP and plugin tools included, and so that hooks and telemetry,
// installed outside it, observe the same shortened result the model will see.
type OutputLimiter struct {
	dir string
}

// NewOutputLimiter saves oversized output under dir, the conversation's
// tool-outputs directory.
func NewOutputLimiter(dir string) *OutputLimiter {
	return &OutputLimiter{dir: dir}
}

// Middleware returns the hook to register with agentcore. Install it innermost
// (last in the middleware slice) so hooks and telemetry observe the same
// shortened result the model will see. It limits results of one text block,
// as tools return text; images and the like pass through.
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

// truncateAndSave saves result and returns its preview.
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
