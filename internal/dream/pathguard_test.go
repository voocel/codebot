package dream

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/voocel/agentcore"
)

// fakeMutationTool records whether the inner tool was reached.
type fakeMutationTool struct {
	executed  int
	validated int
}

func (f *fakeMutationTool) Name() string                                 { return "write" }
func (f *fakeMutationTool) Description() string                          { return "fake" }
func (f *fakeMutationTool) Schema() map[string]any                       { return nil }
func (f *fakeMutationTool) Label() string                                { return "Fake" }
func (f *fakeMutationTool) ReadOnly(_ json.RawMessage) bool              { return false }
func (f *fakeMutationTool) ConcurrencySafe(_ json.RawMessage) bool       { return false }
func (f *fakeMutationTool) ActivityDescription(_ json.RawMessage) string { return "faking" }
func (f *fakeMutationTool) Preview(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}
func (f *fakeMutationTool) Validate(_ context.Context, _ json.RawMessage) agentcore.ValidationResult {
	f.validated++
	return agentcore.ValidationResult{OK: true}
}
func (f *fakeMutationTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	f.executed++
	return json.RawMessage(`"ok"`), nil
}

func args(path string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"file_path": path, "content": "x"})
	return raw
}

func TestPathGuardAllowsInsideRoot(t *testing.T) {
	root := t.TempDir()
	inner := &fakeMutationTool{}
	g := guardPath(inner, root)

	for _, p := range []string{
		"MEMORY.md",                                 // relative
		filepath.Join(root, "topic.md"),             // absolute
		filepath.Join(root, "sub", "deep.md"),       // nested
		filepath.Join(root, "sub", "..", "flat.md"), // cleans back inside
	} {
		if _, err := g.Execute(context.Background(), args(p)); err != nil {
			t.Errorf("Execute(%q) rejected: %v", p, err)
		}
	}
	if inner.executed != 4 {
		t.Fatalf("inner executed %d times, want 4", inner.executed)
	}
}

func TestPathGuardRejectsEscapes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	inner := &fakeMutationTool{}
	g := guardPath(inner, root)

	escapes := []string{
		"../evil.md",
		"..",
		filepath.Join("sub", "..", "..", "out.md"),
		filepath.Dir(root),                     // parent
		filepath.Join(t.TempDir(), "other.md"), // absolute outside
	}
	if runtime.GOOS == "windows" {
		escapes = append(escapes, `Q:\other\evil.md`) // cross-volume
	}
	for _, p := range escapes {
		if _, err := g.Execute(context.Background(), args(p)); err == nil {
			t.Errorf("Execute(%q) allowed, want rejection", p)
		}
	}
	if inner.executed != 0 {
		t.Fatalf("inner executed %d times, want 0", inner.executed)
	}
}

func TestPathGuardRejectsLockFile(t *testing.T) {
	root := t.TempDir()
	g := guardPath(&fakeMutationTool{}, root)

	for _, p := range []string{lockFileName, ".CONSOLIDATE-LOCK"} {
		if _, err := g.Execute(context.Background(), args(p)); err == nil {
			t.Errorf("Execute(%q) allowed, want rejection", p)
		}
	}
}

func TestPathGuardWindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only")
	}
	root := t.TempDir()
	inner := &fakeMutationTool{}
	g := guardPath(inner, root)

	same := filepath.Join(root, "note.md")
	if _, err := g.Execute(context.Background(), args(same)); err != nil {
		t.Fatalf("same-case rejected: %v", err)
	}
	swapped := filepath.Join(swapCase(root), "note.md")
	if _, err := g.Execute(context.Background(), args(swapped)); err != nil {
		t.Fatalf("case-swapped path rejected: %v", err)
	}
}

func swapCase(s string) string {
	out := []rune(s)
	for i, r := range out {
		switch {
		case r >= 'a' && r <= 'z':
			out[i] = r - 32
		case r >= 'A' && r <= 'Z':
			out[i] = r + 32
		}
	}
	return string(out)
}

func TestPathGuardValidateShortCircuits(t *testing.T) {
	root := t.TempDir()
	inner := &fakeMutationTool{}
	g := guardPath(inner, root).(*pathGuarded)

	res := g.Validate(context.Background(), args("../evil.md"))
	if res.OK {
		t.Fatal("Validate allowed an escape")
	}
	if inner.validated != 0 {
		t.Fatal("inner Validate should not run for rejected paths")
	}

	res = g.Validate(context.Background(), args("fine.md"))
	if !res.OK || inner.validated != 1 {
		t.Fatalf("in-root path should delegate: ok=%v delegated=%d", res.OK, inner.validated)
	}
}
