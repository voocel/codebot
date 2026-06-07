package bootstrap

import (
	"context"
	"time"

	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/hooks"
	mcpclient "github.com/voocel/codebot/internal/mcp"
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
	EnvHint       string                            // non-empty when credentials come from environment variable
	PlanSlug      string                            // restored plan slug (empty if no plan)
	PlanPhase     string                            // restored plan phase
	PlanPreMode   string                            // restored plan pre-mode
	Goal          storage.GoalStateEntry            // restored explicit /goal state

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

	assembly, err := buildSessionAssembly(input, services, opts.ToolFactories)
	if err != nil {
		return nil, err
	}

	rt, err := assembleRuntime(input, services, assembly)
	if err != nil {
		return nil, err
	}
	rt.telemetryShutdown = input.telemetryShutdown

	closeStoreOnError = false
	go localtools.CleanOldOutputs()
	return rt, nil
}
