package acp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	agentcore "github.com/voocel/agentcore"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/interact"
)

// Server serves the sessions a client opens, each a conversation of the App.
type Server struct {
	version string
	app     *app.App

	conn *acp.AgentSideConnection

	mu       sync.Mutex
	fs       acp.FileSystemCapabilities // the client's
	sessions map[acp.SessionId]*session
}

var (
	_ acp.Agent       = (*Server)(nil)
	_ acp.AgentLoader = (*Server)(nil)
)

func (s *Server) Initialize(_ context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	s.mu.Lock()
	s.fs = req.ClientCapabilities.Fs
	s.mu.Unlock()
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: acp.PromptCapabilities{Image: true, EmbeddedContext: true},
			McpCapabilities:    acp.McpCapabilities{Http: true},
			SessionCapabilities: acp.SessionCapabilities{
				List:   &acp.SessionListCapabilities{},
				Resume: &acp.SessionResumeCapabilities{},
				Close:  &acp.SessionCloseCapabilities{},
			},
		},
		AuthMethods: []acp.AuthMethod{}, // credentials come from settings.json
		AgentInfo:   &acp.Implementation{Name: "codebot", Title: acp.Ptr("codebot"), Version: s.version},
	}, nil
}

func (s *Server) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (s *Server) NewSession(_ context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	sess, err := s.open("", req.Cwd, req.McpServers)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	return acp.NewSessionResponse{SessionId: sess.id, Modes: sess.modes(), ConfigOptions: sess.configOptions()}, nil
}

// LoadSession answers once the conversation so far has been sent.
func (s *Server) LoadSession(_ context.Context, req acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	sess, err := s.resume(req.SessionId, req.Cwd, req.McpServers)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	sess.replay()
	return acp.LoadSessionResponse{Modes: sess.modes(), ConfigOptions: sess.configOptions()}, nil
}

func (s *Server) ResumeSession(_ context.Context, req acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	sess, err := s.resume(req.SessionId, req.Cwd, req.McpServers)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	return acp.ResumeSessionResponse{Modes: sess.modes(), ConfigOptions: sess.configOptions()}, nil
}

// resume returns session id, opening it unless the client has it open.
func (s *Server) resume(id acp.SessionId, cwd string, servers []acp.McpServer) (*session, error) {
	if sess, err := s.session(id); err == nil {
		return sess, nil
	}
	return s.open(string(id), cwd, servers)
}

// open opens session id, or a new session when id is "".
func (s *Server) open(id, cwd string, servers []acp.McpServer) (*session, error) {
	if !filepath.IsAbs(cwd) {
		return nil, fmt.Errorf("acp: the session's cwd %q is not an absolute path", cwd)
	}
	s.useMCP(servers)
	s.mu.Lock()
	caps := s.fs
	s.mu.Unlock()
	sess := &session{srv: s}
	sess.fs = &EditorFS{conn: s.conn, canRead: caps.ReadTextFile, canWrite: caps.WriteTextFile, logf: log.New(os.Stderr, "", log.LstdFlags).Printf}
	conv, err := s.app.Start(id, cwd, app.Frontend{UI: sess, FS: sess.fs})
	if err != nil {
		return nil, err
	}
	// Set before the session is listed, so before anything asks or reads a
	// file through it.
	sess.id, sess.conv = acp.SessionId(conv.ID()), conv
	sess.fs.sid = sess.id
	s.mu.Lock()
	s.sessions[sess.id] = sess
	s.mu.Unlock()
	return sess, nil
}

// useMCP connects the client's MCP servers beside codebot's own.
func (s *Server) useMCP(servers []acp.McpServer) {
	out := make(map[string]config.MCPServer, len(servers))
	for _, srv := range servers {
		switch {
		case srv.Stdio != nil:
			env := make(map[string]string, len(srv.Stdio.Env))
			for _, v := range srv.Stdio.Env {
				env[v.Name] = v.Value
			}
			out[srv.Stdio.Name] = config.MCPServer{Command: srv.Stdio.Command, Args: srv.Stdio.Args, Env: env}
		case srv.Http != nil:
			headers := make(map[string]string, len(srv.Http.Headers))
			for _, h := range srv.Http.Headers {
				headers[h.Name] = h.Value
			}
			out[srv.Http.Name] = config.MCPServer{Type: "http", URL: srv.Http.Url, Headers: headers}
		default:
			fmt.Fprintln(os.Stderr, "acp: an MCP server over a transport codebot does not offer is left out")
		}
	}
	if len(out) > 0 {
		s.app.UseMCPServers(out)
	}
}

// opened sends what follows the response that opened session id.
func (s *Server) opened(id acp.SessionId) {
	if sess, err := s.session(id); err == nil {
		sess.sendCommands()
	}
}

func (s *Server) session(id acp.SessionId) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok {
		return sess, nil
	}
	return nil, fmt.Errorf("acp: no session %s is open", id)
}

func (s *Server) ListSessions(_ context.Context, req acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	if req.Cursor != nil {
		return acp.ListSessionsResponse{}, errors.New("acp: codebot lists every session at once, so it gave no cursor")
	}
	infos, err := s.app.Sessions()
	if err != nil {
		return acp.ListSessionsResponse{}, err
	}
	out := []acp.SessionInfo{}
	for _, info := range infos {
		if info.MessageCount == 0 || (req.Cwd != nil && !sameDir(*req.Cwd, info.Cwd)) {
			continue
		}
		si := acp.SessionInfo{SessionId: acp.SessionId(info.ID), Cwd: info.Cwd, UpdatedAt: acp.Ptr(info.Updated.UTC().Format(time.RFC3339))}
		if info.FirstMessage != "" {
			si.Title = acp.Ptr(info.FirstMessage)
		}
		out = append(out, si)
	}
	return acp.ListSessionsResponse{Sessions: out}, nil
}

func (s *Server) CloseSession(_ context.Context, req acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	s.mu.Lock()
	sess, ok := s.sessions[req.SessionId]
	delete(s.sessions, req.SessionId)
	s.mu.Unlock()
	if !ok {
		return acp.CloseSessionResponse{}, fmt.Errorf("acp: no session %s is open", req.SessionId)
	}
	sess.close()
	return acp.CloseSessionResponse{}, nil
}

func (s *Server) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	sess, err := s.session(req.SessionId)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	blocks, err := promptBlocks(req.Prompt)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	return sess.prompt(ctx, blocks)
}

// turnResult reports a failed run as a JSON-RPC error because ACP has no
// error stop reason.
func turnResult(run *agentcore.RunEnd) (acp.PromptResponse, error) {
	if run == nil {
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	switch run.Reason {
	case agentcore.EndMaxTurns:
		return acp.PromptResponse{StopReason: acp.StopReasonMaxTurnRequests}, nil
	case agentcore.EndAborted:
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	case agentcore.EndError:
		return acp.PromptResponse{}, errors.New(app.ErrorText(run.Err))
	default:
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
}

// Cancel stops the run; the SDK has cancelled the prompt's context, which
// stops a skill that forks.
func (s *Server) Cancel(_ context.Context, req acp.CancelNotification) error {
	sess, err := s.session(req.SessionId)
	if err != nil {
		return err
	}
	sess.conv.Cancel()
	return nil
}

func (s *Server) SetSessionMode(_ context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	sess, err := s.session(req.SessionId)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	mode, err := interact.ParseMode(string(req.ModeId))
	if err != nil {
		return acp.SetSessionModeResponse{}, fmt.Errorf("acp: %w", err)
	}
	sess.conv.SetMode(mode)
	return acp.SetSessionModeResponse{}, nil
}

func (s *Server) SetSessionConfigOption(_ context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if req.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, errors.New("acp: codebot's options are selects")
	}
	sess, err := s.session(req.ValueId.SessionId)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if err := sess.setOption(req.ValueId.ConfigId, req.ValueId.Value); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: sess.configOptions()}, nil
}

// The methods below are not advertised, so conforming clients don't call
// them.

func (s *Server) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

func sameDir(a, b string) bool {
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	return real(a) == real(b)
}
