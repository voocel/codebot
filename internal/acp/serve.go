// Package acp implements the Agent Client Protocol (ACP) frontend: it lets an
// editor (Zed, JetBrains, Neovim, ...) spawn codebot as a child process and
// drive it over JSON-RPC 2.0 on stdio. It is the fourth frontend alongside the
// TUI, print, and RPC modes — a thin adapter over agent.Session, leaving the
// agentcore kernel and the harness untouched.
//
// Stdout is the protocol channel in this mode; nothing else may write to it
// (logging and diagnostics go to stderr).
package acp

import (
	"os"

	acp "github.com/coder/acp-go-sdk"

	"github.com/voocel/codebot/internal/bootstrap"
)

// Serve runs codebot as an ACP agent over stdio, blocking until the client
// disconnects. fs is the editor-backed WorkspaceFS injected into the runtime's
// tools at boot; Serve binds the live connection to it. Pass nil to keep the
// local filesystem.
func Serve(rt *bootstrap.Runtime, version string, fs *WorkspaceFS) error {
	a := newAgent(rt, version, fs)
	conn := acp.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	a.setConnection(conn) // local helper, not part of the acp.Agent contract
	<-conn.Done()
	return nil
}
