package bootstrap

import (
	"context"
	"time"

	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/cron"
	"github.com/voocel/codebot/internal/dream"
	"github.com/voocel/codebot/internal/goal"
	"github.com/voocel/codebot/internal/hooks"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/plan"
	"github.com/voocel/codebot/internal/plugin"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/storage"
	localtools "github.com/voocel/codebot/internal/tools"
)

// Options controls how runtime bootstraps.
type Options struct {
	Cwd string

	Continue   bool
	Resume     bool
	NonTTYMode bool

	ApprovalMode  string
	ToolFactories []ToolFactory
	ModelFactory  agent.ModelFactory

	// WorkspaceFS is the file backend that read/write/edit operate on, shared
	// by the main agent and every sub-agent. Nil means the local filesystem.
	// Frontends that serve files differently (e.g. the ACP frontend reading
	// editor buffers) inject their own backend here.
	WorkspaceFS agentcoretools.WorkspaceFS
}

// Runtime is the bootstrapped app runtime state.
type Runtime struct {
	Cwd       string
	GitBranch string

	ApprovalEngine *approval.Engine
	TaskRuntime    *task.Runtime
	TeamRegistry   *team.Registry
	TeammateEvents *agent.TeammateEventHub

	Settings      config.Resolved
	ModelName     string // display form: "provider/model" or session-restored name
	Session       *agent.Session
	SessionStore  *storage.Store
	PluginCatalog *plugin.Catalog
	SkillCatalog  *skill.Catalog
	MCPManager    *mcpclient.Manager
	MCPServers    map[string]mcpclient.ServerConfig // for async connection in TUI
	HookRunner    *hooks.Runner                     // nil if no hooks configured
	Dreamer       *dream.Dreamer                    // background memory consolidation; nil in print mode

	// Frontend-neutral session lifecycle, assembled by wireLifecycle.
	// Restored from the session snapshot before any frontend runs.
	PlanStore   *storage.PlanStore
	PlanManager *plan.Manager
	GoalManager *goal.Manager
	CronStore   *cron.Store // nil when the cron tools are absent

	// Worktree sandbox state (Phase 1 /worktree). The cwd-bound tools are not
	// rebuilt on a switch — they resolve against the session's cwd override (see
	// agent.Session.runCtx); originalRoots is captured at boot and restored on
	// exit; activeWorktree is non-nil only while inside a sandbox.
	originalRoots  approval.FilesystemRoots
	activeWorktree *worktreeState

	// stopTeamPump cancels the leader-inbox pump goroutine. Set by
	// assembleRuntime, called by Close so the pump exits before the team
	// registry is torn down.
	stopTeamPump context.CancelFunc

	// telemetryShutdown flushes pending OTLP trace spans on Close; nil when
	// telemetry is disabled.
	telemetryShutdown func(context.Context) error
}

// Close releases runtime resources.
func (r *Runtime) Close() {
	if r.HookRunner != nil {
		r.HookRunner.RunSessionEnd(context.Background())
	}
	if r.TaskRuntime != nil {
		r.TaskRuntime.StopAll()
	}
	// Stop the leader inbox pump before tearing down the team so it doesn't
	// race a closing mailbox.
	if r.stopTeamPump != nil {
		r.stopTeamPump()
	}
	// DeleteTeam closes all mailboxes; skip if no team was ever created.
	if r.TeamRegistry != nil && r.TeamRegistry.HasTeam() {
		_ = r.TeamRegistry.DeleteTeam()
	}
	if r.MCPManager != nil {
		r.MCPManager.Close()
	}
	if r.telemetryShutdown != nil {
		// Bound the final span flush so a hung OTLP backend can't block exit.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = r.telemetryShutdown(ctx)
		cancel()
	}
	if r.Session != nil {
		r.Session.Close()
	}
}

// Boot creates a ready-to-run runtime.
func Boot(opts Options) (*Runtime, error) {
	input, err := resolveInput(opts)
	if err != nil {
		return nil, err
	}

	closeStoreOnError := true
	defer func() {
		if closeStoreOnError && input.sessionStore != nil {
			_ = input.sessionStore.Close()
		}
	}()

	services, err := buildServices(input)
	if err != nil {
		return nil, err
	}

	assembly, err := buildSessionAssembly(input, services, opts.ToolFactories, opts.WorkspaceFS)
	if err != nil {
		return nil, err
	}

	rt, err := assembleRuntime(input, services, assembly)
	if err != nil {
		return nil, err
	}
	rt.telemetryShutdown = input.telemetryShutdown

	if err := wireLifecycle(rt, input); err != nil {
		return nil, err
	}

	closeStoreOnError = false
	go localtools.CleanOldOutputs()
	go rt.CleanWorktreeOrphans()
	return rt, nil
}
