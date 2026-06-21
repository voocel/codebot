package acp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	acp "github.com/coder/acp-go-sdk"
	agentcore "github.com/voocel/agentcore"

	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/bootstrap"
)

// acpAgent adapts a codebot agent.Session to the acp.Agent interface.
//
// MVP scope: one connection serves the single session created at boot. cwd is
// fixed by Boot; session/load, multi-session, and fs/terminal callbacks are
// out of scope (see tasks/acp-frontend.md).
type acpAgent struct {
	rt      *bootstrap.Runtime
	sess    *agent.Session
	version string

	// conn is set once by setConnection after NewAgentSideConnection has
	// already started its inbound reader goroutine, so it is written on one
	// goroutine and read (during a prompt turn) on others. atomic.Pointer
	// gives that publication a happens-before edge without a mutex on the hot
	// event path. sid is set in newAgent before the reader goroutine starts,
	// so it needs no synchronisation.
	conn atomic.Pointer[acp.AgentSideConnection]
	sid  acp.SessionId
	fs   *WorkspaceFS // editor-backed file backend; nil when not injected

	mu           sync.Mutex
	turn         chan turnResult // completion sink for the in-flight prompt turn
	subscribed   bool
	unsub        func()
	pendingEdits map[acp.ToolCallId]editSnapshot // mu-guarded: pre-exec file snapshots for native diffs
}

var _ acp.Agent = (*acpAgent)(nil)

func newAgent(rt *bootstrap.Runtime, version string, fs *WorkspaceFS) *acpAgent {
	a := &acpAgent{
		rt:           rt,
		sess:         rt.Session,
		version:      version,
		sid:          acp.SessionId(rt.Session.SessionID()),
		fs:           fs,
		pendingEdits: make(map[acp.ToolCallId]editSnapshot),
	}
	if fs != nil {
		fs.setSession(a.sid)
	}
	return a
}

func (a *acpAgent) setConnection(c *acp.AgentSideConnection) {
	a.conn.Store(c)
	if a.fs != nil {
		a.fs.bindConn(c)
	}
}

func (a *acpAgent) Initialize(_ context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	// Route file reads/writes through the editor only for the capabilities it
	// advertises; the backend falls back to the local filesystem otherwise.
	if a.fs != nil {
		a.fs.setCaps(req.ClientCapabilities.Fs.ReadTextFile, req.ClientCapabilities.Fs.WriteTextFile)
	}
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{LoadSession: false}, // session/load: stage 2
		AuthMethods:       []acp.AuthMethod{},                        // keys via env, no auth
		AgentInfo:         &acp.Implementation{Name: "codebot", Version: a.version},
	}, nil
}

func (a *acpAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *acpAgent) NewSession(_ context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if err := sameDir(req.Cwd, a.rt.Cwd); err != nil {
		return acp.NewSessionResponse{}, err
	}
	// MVP: codebot's MCP servers come from its own settings.json; the editor's
	// req.McpServers are not wired in yet (stage 2).
	a.bind()
	return acp.NewSessionResponse{SessionId: a.sid}, nil
}

// bind subscribes to session events and installs the editor-backed approver.
// Idempotent so repeated NewSession calls are harmless.
func (a *acpAgent) bind() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.subscribed {
		return
	}
	a.rt.ApprovalEngine.SetApprover(a.approve)
	a.unsub = a.sess.Subscribe(a.onSessionEvent)
	a.subscribed = true
}

func (a *acpAgent) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	ch := make(chan turnResult, 1)
	a.mu.Lock()
	a.turn = ch
	a.mu.Unlock()

	if err := a.dispatchPrompt(req.Prompt); err != nil {
		a.clearTurn()
		return acp.PromptResponse{}, err
	}

	select {
	case r := <-ch:
		if r.err != nil {
			return acp.PromptResponse{}, r.err // ACP has no error stop reason → JSON-RPC error
		}
		return acp.PromptResponse{StopReason: r.stop}, nil
	case <-ctx.Done():
		a.sess.Abort()
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
}

func (a *acpAgent) clearTurn() {
	a.mu.Lock()
	a.turn = nil
	a.mu.Unlock()
}

// dispatchPrompt sends the user message into the session. A lone text block
// goes through Session.Prompt to preserve UserPromptSubmit hook parity;
// anything richer (images, multiple blocks) goes through PromptWithBlocks,
// which does not run that hook.
func (a *acpAgent) dispatchPrompt(blocks []acp.ContentBlock) error {
	if len(blocks) == 1 && blocks[0].Text != nil {
		return a.sess.Prompt(blocks[0].Text.Text)
	}
	out := make([]agentcore.ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			out = append(out, agentcore.TextBlock(b.Text.Text))
		case b.Image != nil:
			out = append(out, agentcore.ImageBlock(b.Image.Data, b.Image.MimeType))
		}
	}
	if len(out) == 0 {
		return fmt.Errorf("acp: prompt has no supported content blocks")
	}
	return a.sess.PromptWithBlocks(out)
}

func (a *acpAgent) Cancel(context.Context, acp.CancelNotification) error {
	a.sess.Abort()
	return nil
}

func (a *acpAgent) SetSessionMode(_ context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	eng := a.rt.ApprovalEngine
	switch strings.ToLower(string(req.ModeId)) {
	case "plan":
		eng.SetPlanMode(true)
	case "strict":
		eng.SetPlanMode(false)
		eng.SetMode(approval.ModeStrict)
	case "balanced", "default":
		eng.SetPlanMode(false)
		eng.SetMode(approval.ModeBalanced)
	case "auto", "accept-edits", "acceptedits":
		eng.SetPlanMode(false)
		eng.SetMode(approval.ModeAuto)
	case "trust", "bypass":
		eng.SetPlanMode(false)
		eng.SetMode(approval.ModeTrust)
	default:
		return acp.SetSessionModeResponse{}, fmt.Errorf("acp: unknown session mode %q", req.ModeId)
	}
	return acp.SetSessionModeResponse{}, nil
}

// Methods below are not supported in the MVP. They are not advertised via
// capabilities, so a conforming client should not call them; we return
// MethodNotFound rather than a silent no-op.

func (a *acpAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound("logout")
}

func (a *acpAgent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, acp.NewMethodNotFound("session/close")
}

func (a *acpAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound("session/list")
}

func (a *acpAgent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound("session/resume")
}

func (a *acpAgent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
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
