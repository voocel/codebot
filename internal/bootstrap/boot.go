package bootstrap

import (
	"context"

	"github.com/voocel/agentcore/task"
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
}

// Close releases runtime resources.
func (r *Runtime) Close() {
	if r.HookRunner != nil {
		r.HookRunner.RunSessionEnd(context.Background())
	}
	if r.TaskRuntime != nil {
		r.TaskRuntime.StopAll()
	}
	if r.MCPManager != nil {
		r.MCPManager.Close()
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

	closeStoreOnError = false
	go localtools.CleanOldOutputs()
	return rt, nil
}
