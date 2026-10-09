package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// failingEditor refuses every read and write.
type failingEditor struct{}

func (failingEditor) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errors.New("editor boom")
}

func (failingEditor) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errors.New("editor boom")
}

// Falling back to disk would desync the file from the editor buffer.
func TestEditorFS_WriteFailsHardOnEditorError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.go")
	ws := &EditorFS{conn: failingEditor{}, sid: "s1", canWrite: true}
	if err := ws.WriteFile(context.Background(), path, []byte("data"), 0o644); err == nil {
		t.Fatal("expected error when editor write fails, got nil")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("editor write failure must not fall back to disk; file present: %v", err)
	}
}
