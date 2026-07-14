package dream

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/voocel/agentcore"
)

// fileMutationTool is the method set shared by agentcore's WriteTool and
// EditTool that the guard must preserve: the agent loop consults these
// optional interfaces for labeling, preview, validation, and concurrency
// scheduling. Embedding it promotes everything except what we override.
type fileMutationTool interface {
	agentcore.Tool
	agentcore.ToolLabeler
	agentcore.Previewer
	agentcore.Validator
	agentcore.ReadOnlyTool
	agentcore.ConcurrencySafeTool
	agentcore.ActivityDescriber
}

// pathGuarded confines a write/edit tool to a root directory. The dream
// subagent's loop has no approval gate, so this is the hard boundary —
// the system prompt merely hints at it, this enforces it.
type pathGuarded struct {
	fileMutationTool
	root string
}

// guardPath wraps a file-mutation tool so it rejects any file_path that
// resolves outside root (or targets the consolidation lock file).
func guardPath(t fileMutationTool, root string) agentcore.Tool {
	return &pathGuarded{fileMutationTool: t, root: filepath.Clean(root)}
}

// Validate short-circuits out-of-root paths before preview/execute so the
// LLM gets a self-correctable tool_result instead of a hard error.
func (g *pathGuarded) Validate(ctx context.Context, args json.RawMessage) agentcore.ValidationResult {
	if err := g.check(args); err != nil {
		return agentcore.ValidationResult{OK: false, Message: err.Error()}
	}
	return g.fileMutationTool.Validate(ctx, args)
}

// Execute re-checks even though Validate already did: Validate is an
// optional loop optimization, Execute is the boundary.
func (g *pathGuarded) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	if err := g.check(args); err != nil {
		return nil, err
	}
	return g.fileMutationTool.Execute(ctx, args)
}

func (g *pathGuarded) check(args json.RawMessage) error {
	var p struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	abs := p.FilePath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(g.root, abs)
	}
	abs = filepath.Clean(abs)
	if !within(g.root, abs) {
		return fmt.Errorf("%s may only touch files inside the memory directory %s (got %q)", g.Name(), g.root, p.FilePath)
	}
	if strings.EqualFold(filepath.Base(abs), lockFileName) {
		return fmt.Errorf("the consolidation lock file is off-limits")
	}
	return nil
}

// within reports whether path is root itself or inside it. Rel handles
// `..` escapes and cross-volume paths (which make it error). Windows
// paths compare case-insensitively.
func within(root, path string) bool {
	if runtime.GOOS == "windows" {
		root, path = strings.ToLower(root), strings.ToLower(path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
