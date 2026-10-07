package acp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	acp "github.com/coder/acp-go-sdk"
	agentcore "github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/interact"
)

// Server serves only the conversation opened at boot; session/load and
// multiple sessions are not supported.
type Server struct {
	version string
	app     *app.App
	fs      *EditorFS

	// Atomic because Serve sets it after the connection has started reading.
	conn atomic.Pointer[acp.AgentSideConnection]

	mu           sync.Mutex
	pendingEdits map[acp.ToolCallId]editSnapshot // pre-exec file snapshots for native diffs
}

var _ acp.Agent = (*Server)(nil)

func (s *Server) sessionID() acp.SessionId { return acp.SessionId(s.app.Current().ID()) }

func (s *Server) Initialize(_ context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	s.fs.setCaps(req.ClientCapabilities.Fs.ReadTextFile, req.ClientCapabilities.Fs.WriteTextFile)
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{LoadSession: false},
		AuthMethods:       []acp.AuthMethod{}, // credentials come from settings.json
		AgentInfo:         &acp.Implementation{Name: "codebot", Version: s.version},
	}, nil
}

func (s *Server) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (s *Server) NewSession(_ context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if err := sameDir(req.Cwd, s.app.Cwd()); err != nil {
		return acp.NewSessionResponse{}, err
	}
	// MCP servers come from codebot's settings; req.McpServers is ignored.
	return acp.NewSessionResponse{SessionId: s.sessionID(), Modes: s.sessionModes()}, nil
}

var modeDescriptions = map[interact.Mode]string{
	interact.ModeStrict:      "Ask before edits; block commands",
	interact.ModeBalanced:    "Ask before edits and commands",
	interact.ModeAcceptEdits: "Edit freely; ask before commands",
	interact.ModeTrust:       "Run everything without asking",
}

func (s *Server) sessionModes() *acp.SessionModeState {
	modes := make([]acp.SessionMode, 0, len(interact.Modes))
	for _, m := range interact.Modes {
		modes = append(modes, acp.SessionMode{
			Id:          acp.SessionModeId(m),
			Name:        string(m),
			Description: acp.Ptr(modeDescriptions[m]),
		})
	}
	return &acp.SessionModeState{AvailableModes: modes, CurrentModeId: acp.SessionModeId(s.app.Mode())}
}

// Prompt answers only after every update of the turn has been sent.
func (s *Server) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	blocks, err := promptBlocks(req.Prompt)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	conv := s.app.Current()
	if err := conv.Submit(ctx, blocks); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := conv.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			conv.Cancel()
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}
	return turnResult(conv.Status().LastRun)
}

func promptBlocks(blocks []acp.ContentBlock) ([]litellm.Block, error) {
	out := make([]litellm.Block, 0, len(blocks))
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			out = append(out, litellm.Text(b.Text.Text))
		case b.Image != nil:
			data, err := base64.StdEncoding.DecodeString(b.Image.Data)
			if err != nil {
				return nil, fmt.Errorf("acp: image: %w", err)
			}
			out = append(out, litellm.ImageBlock{Data: data, MIME: b.Image.MimeType})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("acp: prompt has no supported content blocks")
	}
	return out, nil
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

func (s *Server) Cancel(context.Context, acp.CancelNotification) error {
	s.app.Current().Cancel()
	return nil
}

func (s *Server) SetSessionMode(_ context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	mode, err := interact.ParseMode(string(req.ModeId))
	if err != nil {
		return acp.SetSessionModeResponse{}, fmt.Errorf("acp: %w", err)
	}
	s.app.SetMode(mode)
	return acp.SetSessionModeResponse{}, nil
}

// The methods below are not advertised, so conforming clients don't call
// them.

func (s *Server) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound("logout")
}

func (s *Server) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, acp.NewMethodNotFound("session/close")
}

func (s *Server) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound("session/list")
}

func (s *Server) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound("session/resume")
}

func (s *Server) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound("session/set_config_option")
}

func sameDir(reqDir, rtDir string) error {
	a, err1 := canonDir(reqDir)
	b, err2 := canonDir(rtDir)
	if err1 != nil || err2 != nil || a != b {
		return fmt.Errorf("acp: session cwd %q does not match codebot working directory %q", reqDir, rtDir)
	}
	return nil
}

func canonDir(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}
