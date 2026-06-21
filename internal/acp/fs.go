package acp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sync"

	acp "github.com/coder/acp-go-sdk"
	agentcoretools "github.com/voocel/agentcore/tools"
)

// WorkspaceFS is the ACP-backed agentcore WorkspaceFS: read/write of text files
// is routed to the editor (fs/read_text_file, fs/write_text_file) so the agent
// sees unsaved buffer contents and writes land back in the editor. Everything
// else — stat, directory listing, mkdir, and any file the editor can't serve as
// text (binary, images, or when the client lacks the capability) — falls back
// to the local filesystem via the embedded OSWorkspaceFS.
//
// The connection and session id are bound lazily: the backend is constructed
// before Boot (so it can be injected into the tools), while the ACP connection
// only exists once Serve starts. Until bound, every method transparently uses
// the OS fallback.
type WorkspaceFS struct {
	agentcoretools.OSWorkspaceFS // fallback for stat/readdir/mkdir and non-text reads

	mu       sync.RWMutex
	conn     *acp.AgentSideConnection
	sid      acp.SessionId
	canRead  bool
	canWrite bool
}

var _ agentcoretools.WorkspaceFS = (*WorkspaceFS)(nil)

// NewWorkspaceFS creates an unbound ACP backend. Until bindConn/setSession/
// setCaps are called it behaves exactly like the local filesystem.
func NewWorkspaceFS() *WorkspaceFS { return &WorkspaceFS{} }

func (w *WorkspaceFS) bindConn(c *acp.AgentSideConnection) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn = c
}

func (w *WorkspaceFS) setSession(sid acp.SessionId) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sid = sid
}

// setCaps records the editor's advertised fs capabilities from initialize.
func (w *WorkspaceFS) setCaps(canRead, canWrite bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.canRead = canRead
	w.canWrite = canWrite
}

func (w *WorkspaceFS) readReady() (*acp.AgentSideConnection, acp.SessionId, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.conn, w.sid, w.canRead && w.conn != nil && w.sid != ""
}

func (w *WorkspaceFS) writeReady() (*acp.AgentSideConnection, acp.SessionId, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.conn, w.sid, w.canWrite && w.conn != nil && w.sid != ""
}

// Stat reports metadata. For a file the editor can serve as text it synthesizes
// a FileInfo whose Version is a hash of the *current buffer* — so the
// read-before-write check tracks unsaved edits (disk mtime would not change for
// them) and the agent can stat a buffer that has no file on disk yet. Anything
// the editor can't serve as text (directories, binaries, images, or when
// unbound/uncapable) falls through to the local filesystem.
func (w *WorkspaceFS) Stat(ctx context.Context, path string) (agentcoretools.FileInfo, error) {
	content, ok := w.tryReadText(ctx, path)
	if !ok {
		return w.OSWorkspaceFS.Stat(ctx, path)
	}
	fi := agentcoretools.FileInfo{
		Name:    filepath.Base(path),
		Size:    int64(len(content)),
		Mode:    0o644,
		Version: hashContent(content),
	}
	// Best-effort real mode/mtime when the file also exists on disk; harmless
	// when it doesn't (a buffer-only new file). Version, not mtime, drives the
	// stale check whenever it is set, so an approximate mtime here is fine.
	if osInfo, err := w.OSWorkspaceFS.Stat(ctx, path); err == nil {
		fi.Mode = osInfo.Mode
		fi.ModTime = osInfo.ModTime
	}
	return fi, nil
}

// Open returns the editor buffer (if available) as a reader, else the OS file.
//
// Reads keep a silent OS fallback on purpose: it is how images and binaries are
// served (the editor's text endpoint errors on them, and we must read the bytes
// to sniff/decode), and how brand-new or editor-unknown files are read. A read
// is non-destructive, so degrading to the on-disk copy is safe — unlike a write
// (see WriteFile).
func (w *WorkspaceFS) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if data, ok := w.tryReadText(ctx, path); ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return w.OSWorkspaceFS.Open(ctx, path)
}

// ReadFile returns the editor buffer (if available), else the OS file.
func (w *WorkspaceFS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if data, ok := w.tryReadText(ctx, path); ok {
		return data, nil
	}
	return w.OSWorkspaceFS.ReadFile(ctx, path)
}

// WriteFile writes through the editor when it is bound and advertises the
// capability; otherwise it writes the local file.
//
// Unlike reads, a failed editor write is NOT silently retried against disk: the
// editor is the source of truth in that mode, so writing behind its back would
// desync the buffer and the file and leave the user thinking the edit landed in
// the editor. We surface the error instead. (Falling back is only correct when
// the editor was never going to handle the write — unbound or no capability.)
func (w *WorkspaceFS) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	conn, sid, ok := w.writeReady()
	if !ok {
		return w.OSWorkspaceFS.WriteFile(ctx, path, data, perm)
	}
	if _, err := conn.WriteTextFile(ctx, acp.WriteTextFileRequest{SessionId: sid, Path: path, Content: string(data)}); err != nil {
		return fmt.Errorf("acp: write_text_file %s: %w", path, err)
	}
	return nil
}

// tryReadText asks the editor for the file as text. It returns ok=false (so the
// caller falls back to the OS) when the backend is unbound, the client lacks the
// capability, or the editor can't serve the file as text (binary/image/missing).
func (w *WorkspaceFS) tryReadText(ctx context.Context, path string) ([]byte, bool) {
	conn, sid, ok := w.readReady()
	if !ok {
		return nil, false
	}
	resp, err := conn.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: sid, Path: path})
	if err != nil {
		return nil, false
	}
	return []byte(resp.Content), true
}

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
