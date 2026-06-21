package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// fakeConn is a controllable acpFileConn for exercising the editor read/write
// paths without a real ACP transport.
type fakeConn struct {
	read  func(path string) (string, error)
	write func(path, content string) error
}

func (f fakeConn) ReadTextFile(_ context.Context, p acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	if f.read == nil {
		return acp.ReadTextFileResponse{}, errors.New("no read handler")
	}
	c, err := f.read(p.Path)
	return acp.ReadTextFileResponse{Content: c}, err
}

func (f fakeConn) WriteTextFile(_ context.Context, p acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	if f.write == nil {
		return acp.WriteTextFileResponse{}, nil
	}
	return acp.WriteTextFileResponse{}, f.write(p.Path, p.Content)
}

// A failed editor write must surface the error, never silently fall back to
// disk — otherwise the on-disk file and the editor buffer desync.
func TestWorkspaceFS_WriteFailsHardOnEditorError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	ws := &WorkspaceFS{
		conn:     fakeConn{write: func(string, string) error { return errors.New("editor boom") }},
		sid:      "s1",
		canWrite: true,
	}
	if err := ws.WriteFile(context.Background(), path, []byte("data"), 0o644); err == nil {
		t.Fatal("expected error when editor write fails, got nil")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("editor write failure must not fall back to disk; file present: %v", err)
	}
}

func TestWorkspaceFS_TextForDiff(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	onDisk := filepath.Join(dir, "ondisk.go")
	if err := os.WriteFile(onDisk, []byte("disk-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.go")

	editorOK := &WorkspaceFS{conn: fakeConn{read: func(string) (string, error) { return "buffer", nil }}, sid: "s", canRead: true}
	editorErr := &WorkspaceFS{conn: fakeConn{read: func(string) (string, error) { return "", errors.New("not text") }}, sid: "s", canRead: true}
	unbound := &WorkspaceFS{}

	t.Run("editor read success is reliable buffer", func(t *testing.T) {
		s := editorOK.textForDiff(ctx, onDisk)
		if !s.reliable || !s.exists || s.text != "buffer" {
			t.Fatalf("got %+v", s)
		}
	})
	// The case codex flagged: editor errors but the file exists on disk. The
	// disk copy may not match the unsaved buffer, so it must NOT be trusted.
	t.Run("editor error with file on disk is unreliable", func(t *testing.T) {
		if s := editorErr.textForDiff(ctx, onDisk); s.reliable {
			t.Fatalf("disk copy must not be trusted as the buffer: %+v", s)
		}
	})
	t.Run("editor error with missing file is reliable new-file", func(t *testing.T) {
		s := editorErr.textForDiff(ctx, missing)
		if !s.reliable || s.exists {
			t.Fatalf("missing file should be a reliable new-file snapshot: %+v", s)
		}
	})
	t.Run("unbound reads disk reliably", func(t *testing.T) {
		s := unbound.textForDiff(ctx, onDisk)
		if !s.reliable || !s.exists || s.text != "disk-old" {
			t.Fatalf("got %+v", s)
		}
	})
	t.Run("unbound missing file is reliable new-file", func(t *testing.T) {
		s := unbound.textForDiff(ctx, missing)
		if !s.reliable || s.exists {
			t.Fatalf("got %+v", s)
		}
	})
}
