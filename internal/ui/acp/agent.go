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

// Server adapts the App to the acp.Agent interface. One connection serves the
// conversation opened at boot; session/load and multiple sessions are out of
// scope.
type Server struct {
	version string
	app     *app.App
	fs      *EditorFS

	// conn is set by Serve after the connection has started reading, so it
	// is read on other goroutines.
	conn atomic.Pointer[acp.AgentSideConnection]

	mu           sync.Mutex
	pendingEdits map[acp.ToolCallId]editSnapshot // pre-exec file snapshots for native diffs
}

var _ acp.Agent = (*Server)(nil)

func (s *Server) sessionID() acp.SessionId { return acp.SessionId(s.app.Current().ID()) }

func (s *Server) Initialize(_ context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	// Route file reads/writes through the editor only for the capabilities it
	// advertises; the backend falls back to the local filesystem otherwise.
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
	// MCP servers come from codebot's own settings; the editor's
	// req.McpServers are not wired in.
	return acp.NewSessionResponse{SessionId: s.sessionID(), Modes: s.sessionModes()}, nil
}

// modeDescriptions explains each permission mode in the editor's picker.
var modeDescriptions = map[interact.Mode]string{
	interact.ModeStrict:      "Ask before edits; block commands",
	interact.ModeBalanced:    "Ask before edits and commands",
	interact.ModeAcceptEdits: "Edit freely; ask before commands",
	interact.ModeTrust:       "Run everything without asking",
}

// sessionModes advertises the permission modes as ACP session modes; mode
// ids are the interact.Mode values.
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

// Prompt submits the user's message and answers once the conversation is idle
// again, after every update of the turn has been sent.
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

// promptBlocks converts the prompt's text and image blocks.
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

// turnResult maps how the last run ended to the prompt's response. ACP has
// no error stop reason, so a failed run is a JSON-RPC error.
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

// Methods below are not supported. They are not advertised via capabilities,
// so a conforming client should not call them; we return MethodNotFound
// rather than a silent no-op.

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

// sameDir reports whether two paths resolve to the same directory, tolerant of
// symlinks, relative paths, and trailing slashes.
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
