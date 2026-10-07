// Package acp is the Agent Client Protocol frontend: an editor runs codebot
// as a child process and drives it over JSON-RPC on stdio. Stdout is the
// protocol channel, so nothing else may write to it.
package acp

import (
	"context"
	"fmt"
	"os"

	acp "github.com/coder/acp-go-sdk"
	agentcoretools "github.com/voocel/agentcore/tools"

	"github.com/voocel/codebot/internal/app"
)

func NewServer(version string) *Server {
	return &Server{version: version, fs: NewEditorFS(), pendingEdits: make(map[acp.ToolCallId]editSnapshot)}
}

func (s *Server) FS() agentcoretools.FS { return s.fs }

func (s *Server) Serve(a *app.App) error {
	s.app = a
	s.fs.setSession(s.sessionID())
	unsubscribe := a.Subscribe(s.onEvent)
	defer unsubscribe()
	// MCP tools join the session as their servers connect.
	go func() {
		report := a.Connect(context.Background())
		for _, e := range report.Errors {
			fmt.Fprintf(os.Stderr, "mcp: %s\n", e)
		}
		for _, name := range report.Login {
			fmt.Fprintf(os.Stderr, "mcp: %s needs a login: run /mcp login %s in codebot\n", name, name)
		}
	}()
	conn := acp.NewAgentSideConnection(s, os.Stdout, os.Stdin)
	s.conn.Store(conn)
	s.fs.bindConn(conn)
	<-conn.Done()
	return nil
}
