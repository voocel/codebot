package acp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"

	acp "github.com/coder/acp-go-sdk"
	agentcoretools "github.com/voocel/agentcore/tools"
)

// acpFileConn lets tests replace *acp.AgentSideConnection with a fake.
type acpFileConn interface {
	ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error)
	WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error)
}

// EditorFS routes text file reads and writes to the editor, so the agent sees
// unsaved buffers and its writes land in them. Everything else, including
// files the editor can't serve as text, uses the local filesystem.
//
// It is built before Boot, while the connection only exists once Serve
// starts; until bound it behaves like the local filesystem.
type EditorFS struct {
	agentcoretools.OSFS

	logf func(format string, args ...any) // nil = silent

	mu       sync.RWMutex
	conn     acpFileConn
	sid      acp.SessionId
	canRead  bool
	canWrite bool
}

var _ agentcoretools.FS = (*EditorFS)(nil)

// NewEditorFS logs to stderr because stdout is the protocol channel.
func NewEditorFS() *EditorFS {
	return &EditorFS{logf: log.New(os.Stderr, "", log.LstdFlags).Printf}
}

func (w *EditorFS) bindConn(c *acp.AgentSideConnection) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn = c
}

func (w *EditorFS) setSession(sid acp.SessionId) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sid = sid
}

func (w *EditorFS) setCaps(canRead, canWrite bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.canRead = canRead
	w.canWrite = canWrite
}

func (w *EditorFS) readReady() (acpFileConn, acp.SessionId, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.conn, w.sid, w.canRead && w.conn != nil && w.sid != ""
}

func (w *EditorFS) writeReady() (acpFileConn, acp.SessionId, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.conn, w.sid, w.canWrite && w.conn != nil && w.sid != ""
}

// Stat sets Version to a hash of the editor buffer, so the read-before-write
// check sees unsaved edits that don't change the disk mtime, and a buffer with
// no file on disk can still be stat'ed.
func (w *EditorFS) Stat(ctx context.Context, path string) (agentcoretools.FileInfo, error) {
	content, ok := w.tryReadText(ctx, path)
	if !ok {
		return w.OSFS.Stat(ctx, path)
	}
	fi := agentcoretools.FileInfo{
		Name:    filepath.Base(path),
		Size:    int64(len(content)),
		Mode:    0o644,
		Version: hashContent(content),
	}
	// Version drives the stale check, so the disk mtime is only informative.
	if osInfo, err := w.OSFS.Stat(ctx, path); err == nil {
		fi.Mode = osInfo.Mode
		fi.ModTime = osInfo.ModTime
	}
	return fi, nil
}

// Open falls back to disk on purpose: images and binaries are read that way,
// since the editor's text endpoint rejects them. A read is harmless; a write
// is not (see WriteFile).
func (w *EditorFS) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if data, ok := w.tryReadText(ctx, path); ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return w.OSFS.Open(ctx, path)
}

func (w *EditorFS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if data, ok := w.tryReadText(ctx, path); ok {
		return data, nil
	}
	return w.OSFS.ReadFile(ctx, path)
}

// WriteFile does not retry a failed editor write on disk: the editor owns the
// file, and writing behind its back would desync the buffer from the file.
func (w *EditorFS) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	conn, sid, ok := w.writeReady()
	if !ok {
		return w.OSFS.WriteFile(ctx, path, data, perm)
	}
	if _, err := conn.WriteTextFile(ctx, acp.WriteTextFileRequest{SessionId: sid, Path: path, Content: string(data)}); err != nil {
		return fmt.Errorf("acp: write_text_file %s: %w", path, err)
	}
	return nil
}

// tryReadText returns false when the caller should read from disk. ACP error
// codes can't tell "not a text file" from a real read failure, so any error
// falls back to disk, which may miss unsaved edits. Conforming clients such as
// Zed return content for every text file, so in practice an error means a
// binary or missing file.
func (w *EditorFS) tryReadText(ctx context.Context, path string) ([]byte, bool) {
	conn, sid, ok := w.readReady()
	if !ok {
		return nil, false
	}
	resp, err := conn.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: sid, Path: path})
	if err != nil {
		// Logged because the agent may now read stale disk bytes instead of
		// the live buffer.
		if w.logf != nil {
			w.logf("acp: fs/read_text_file failed for %s, falling back to local filesystem: %v", path, err)
		}
		return nil, false
	}
	return []byte(resp.Content), true
}

// diffSnapshot with reliable=false must not be rendered: a wrong diff is
// worse than none.
type diffSnapshot struct {
	text     string
	exists   bool
	reliable bool
}

// textForDiff, unlike ReadFile, never passes a disk copy off as the editor
// buffer: if the editor errors on a file that exists on disk, the snapshot is
// unreliable. A file missing from both is a reliable new file.
func (w *EditorFS) textForDiff(ctx context.Context, path string) diffSnapshot {
	conn, sid, hasCap := w.readReady()
	if !hasCap {
		data, err := w.OSFS.ReadFile(ctx, path)
		switch {
		case err == nil:
			return diffSnapshot{text: string(data), exists: true, reliable: true}
		case os.IsNotExist(err):
			return diffSnapshot{reliable: true}
		default:
			return diffSnapshot{}
		}
	}
	if resp, err := conn.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: sid, Path: path}); err == nil {
		return diffSnapshot{text: resp.Content, exists: true, reliable: true}
	}
	// The editor errored: only a file missing on disk is safely new.
	if _, err := w.OSFS.Stat(ctx, path); os.IsNotExist(err) {
		return diffSnapshot{reliable: true}
	}
	return diffSnapshot{}
}

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
