package approval

import (
	"cmp"
	"context"
	"errors"

	"github.com/voocel/agentcore"
	agentcoretools "github.com/voocel/agentcore/tools"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/permission"
)

// Config is what an Engine is made from.
type Config struct {
	Cwd   string
	Mode  interact.Mode
	Rules *RuleSet
	Roots FilesystemRoots
	// UI is asked whatever the mode does not allow on its own.
	UI      interact.UI
	OnAudit func(AuditEntry)
}

// Engine decides tool calls and hook commands. Only its mode and the
// approvals given while it runs change; what one conversation allows on top
// travels with its requests, see Middleware.
type Engine struct {
	kernel *permission.Engine
}

func NewEngine(cfg Config) (*Engine, error) {
	store, err := permission.NewStore(config.ApprovalsPath(cfg.Cwd))
	if err != nil {
		return nil, err
	}
	var approver permission.Approver
	if cfg.UI != nil {
		approver = func(ctx context.Context, p permission.Prompt) (permission.Choice, error) {
			return cfg.UI.Approve(ctx, approvalFor(p))
		}
	}
	return &Engine{kernel: permission.NewEngine(permission.EngineConfig{
		Workspace: cfg.Cwd,
		Mode:      cfg.Mode,
		Rules:     cfg.Rules,
		Roots:     cfg.Roots,
		Store:     store,
		Approver:  approver,
		OnAudit:   cfg.OnAudit,
		Classifier: func(req permission.Request) permission.Classification {
			return classify(firstNonEmpty(req.Workspace, cfg.Cwd), req)
		},
	})}, nil
}

// Mode reports the permission mode.
func (e *Engine) Mode() interact.Mode { return e.kernel.Mode() }

// SetMode switches the permission mode.
func (e *Engine) SetMode(mode interact.Mode) { e.kernel.SetMode(mode) }

// Middleware decides the tool calls of one conversation, refusing those it
// does not allow. grants returns what the conversation allows beyond the mode
// at the time of each call, such as the tools of the skills its run invoked;
// meta returns how the engine sees the tools that classify themselves, such
// as MCP tools.
func (e *Engine) Middleware(grants func() []Rule, meta func(tool string) permission.Metadata) agentcore.ToolMiddleware {
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolFunc) (agentcore.Result, error) {
		decision, err := e.kernel.Decide(ctx, permission.Request{
			ToolID:    call.ID,
			ToolName:  call.Name,
			ToolLabel: call.Tool.Label,
			Args:      call.Args,
			Metadata:  meta(call.Name),
			// Paths resolve against the directory the tool runs in, which
			// moves with a worktree entered mid-run.
			Workspace: agentcoretools.CwdFromContext(ctx),
			Grants:    grants(),
		})
		if err != nil {
			return agentcore.Result{}, err
		}
		if !decision.Allowed() {
			return agentcore.ErrorResult(cmp.Or(decision.Reason, "tool execution denied")), nil
		}
		return next(ctx, call)
	}
}

// ApproveHook decides whether a hook command may run.
func (e *Engine) ApproveHook(ctx context.Context, req HookRequest) error {
	decision, err := e.kernel.Decide(ctx, req.request())
	if err != nil {
		return err
	}
	if !decision.Allowed() {
		return errors.New(decision.Reason)
	}
	return nil
}

// approvalFor is what the user is shown for a kernel prompt.
func approvalFor(p permission.Prompt) interact.Approval {
	a := interact.Approval{
		ToolID:       p.ToolID,
		Tool:         p.Tool,
		Summary:      p.Summary,
		Reason:       p.Reason,
		OutsideRoots: p.OutsideRoots,
		OnceOnly:     p.OnceOnly,
	}
	if p.Tool == "bash" {
		a.Warning = destructiveCommandWarning(p.Summary)
	}
	return a
}
