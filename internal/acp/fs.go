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

// acpFileConn is the slice of the ACP connection this backend needs: the editor
// text file callbacks. Depending on it (rather than the concrete
// *acp.AgentSideConnection) decouples the backend from the SDK transport and
// lets tests inject a fake. *acp.AgentSideConnection satisfies it.
type acpFileConn interface {
	ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error)
	WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error)
}

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

	logf func(format string, args ...any) // diagnostics sink (stderr); nil = silent

	mu       sync.RWMutex
	conn     acpFileConn
	sid      acp.SessionId
	canRead  bool
	canWrite bool
}

var _ agentcoretools.WorkspaceFS = (*WorkspaceFS)(nil)

// NewWorkspaceFS creates an unbound ACP backend. Until bindConn/setSession/
// setCaps are called it behaves exactly like the local filesystem.
//
// Diagnostics go to stderr (log is concurrency-safe and stdout is the protocol
// channel). The fallback to disk on an editor read error stays, but is no longer
// silent — it is the only signal that the editor's buffer view was bypassed.
func NewWorkspaceFS() *WorkspaceFS {
	return &WorkspaceFS{logf: log.New(os.Stderr, "", log.LstdFlags).Printf}
}

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

func (w *WorkspaceFS) readReady() (acpFileConn, acp.SessionId, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.conn, w.sid, w.canRead && w.conn != nil && w.sid != ""
}

func (w *WorkspaceFS) writeReady() (acpFileConn, acp.SessionId, bool) {
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
// capability, or the editor returns any error.
//
// ACP does not document read error conditions and RequestError carries only the
// seven standard JSON-RPC codes — there is no semantic code distinguishing "not
// a text file" (image/binary, which MUST fall back to read the bytes) from a
// transient read failure. So both are treated alike: fall back to disk. Known
// limitation of that conflation: if the editor returns an error for a text file
// that actually has an unsaved buffer, we serve the on-disk copy instead, which
// can miss unsaved edits. In practice a conforming client (e.g. Zed) returns
// content for any text file open or not, so an error here means non-text / truly
// missing / client fault — for the first two, disk is correct or equally absent.
func (w *WorkspaceFS) tryReadText(ctx context.Context, path string) ([]byte, bool) {
	conn, sid, ok := w.readReady()
	if !ok {
		return nil, false
	}
	resp, err := conn.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: sid, Path: path})
	if err != nil {
		// Editor advertised the capability but couldn't serve the file as text
		// (non-text file, or a real read failure — ACP gives no way to tell).
		// We fall back to disk, but record it: this is the one case where the
		// agent may be reading stale on-disk bytes instead of the live buffer.
		if w.logf != nil {
			w.logf("acp: fs/read_text_file failed for %s, falling back to local filesystem: %v", path, err)
		}
		return nil, false
	}
	return []byte(resp.Content), true
}

// diffSnapshot is a file's content captured for native-diff rendering, tagged
// with whether it is trustworthy. reliable=false means "do not render a diff
// from this" — a wrong before/after is worse than no diff at all.
type diffSnapshot struct {
	text     string
	exists   bool
	reliable bool
}

// textForDiff reads a file specifically for native-diff rendering. Unlike
// ReadFile/tryReadText it never passes a disk copy off as the editor buffer:
// when the editor read path is advertised but errors on an existing file, the
// disk contents may differ from the unsaved buffer, so the snapshot is marked
// unreliable and the caller skips the diff. A file absent on disk (and from the
// editor) is a reliable new-file snapshot (exists=false), which renders as a
// new-file diff.
func (w *WorkspaceFS) textForDiff(ctx context.Context, path string) diffSnapshot {
	conn, sid, hasCap := w.readReady()
	if !hasCap {
		// No editor read path at all → the OS copy is the source of truth.
		data, err := w.OSWorkspaceFS.ReadFile(ctx, path)
		switch {
		case err == nil:
			return diffSnapshot{text: string(data), exists: true, reliable: true}
		case os.IsNotExist(err):
			return diffSnapshot{reliable: true} // exists=false → new-file diff
		default:
			return diffSnapshot{} // unreadable → unreliable, skip
		}
	}
	if resp, err := conn.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: sid, Path: path}); err == nil {
		return diffSnapshot{text: resp.Content, exists: true, reliable: true}
	}
	// Capability advertised but the editor errored: can't distinguish "truly new
	// file" from "editor couldn't serve an existing file". Only the former is a
	// safe new-file diff — confirm against disk. If it exists on disk, the disk
	// copy may not match the unsaved buffer, so treat as unreliable.
	if _, err := w.OSWorkspaceFS.Stat(ctx, path); os.IsNotExist(err) {
		return diffSnapshot{reliable: true} // exists=false → new-file diff
	}
	return diffSnapshot{} // unreliable, skip
}

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
