// Package acp implements the Agent Client Protocol (ACP) frontend: it lets an
// editor (Zed, JetBrains, Neovim, ...) spawn codebot as a child process and
// drive it over JSON-RPC 2.0 on stdio.
//
// Stdout is the protocol channel in this mode; nothing else may write to it
// (logging and diagnostics go to stderr).
package acp

import (
	"context"
	"fmt"
	"os"

	acp "github.com/coder/acp-go-sdk"
	agentcoretools "github.com/voocel/agentcore/tools"

	"github.com/voocel/codebot/internal/app"
)

// NewServer creates the ACP frontend. It is the interact.UI the App boots
// with, and FS is its editor-backed file backend; Serve then runs it.
func NewServer(version string) *Server {
	return &Server{version: version, fs: NewEditorFS(), pendingEdits: make(map[acp.ToolCallId]editSnapshot)}
}

// FS routes the file tools through the editor, so they see unsaved buffers.
// Until Serve binds the connection it is the local filesystem.
func (s *Server) FS() agentcoretools.FS { return s.fs }

// Serve runs the ACP agent over stdio until the client disconnects.
func (s *Server) Serve(a *app.App) error {
	s.app = a
	s.fs.setSession(s.sessionID())
	unsubscribe := a.Subscribe(s.onEvent)
	defer unsubscribe()
	// As in the TUI, the session takes plugins and MCP tools up as they
	// are fetched and connect.
	go func() {
		r := a.Connect(context.Background())
		for _, e := range r.FetchErrors {
			fmt.Fprintf(os.Stderr, "plugins: %s\n", e)
		}
		for _, e := range r.MCP.Errors {
			fmt.Fprintf(os.Stderr, "mcp: %s\n", e)
		}
	}()
	conn := acp.NewAgentSideConnection(s, os.Stdout, os.Stdin)
	s.conn.Store(conn)
	s.fs.bindConn(conn)
	<-conn.Done()
	return nil
}
