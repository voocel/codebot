package bootstrap

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/subagent"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
)

// subAgentDeps holds everything needed to build the sub-agent runtime.
type subAgentDeps struct {
	Cwd           string
	Model         agentcore.ChatModel // main agent's model (default for inherit)
	AllTools      []agentcore.Tool    // main agent's tools BEFORE subagent is appended
	ContextWindow int

	// For creating alternative models (e.g. a cheaper model for explore,
	// or arbitrary models named in a custom agent's frontmatter).
	CreateModel agent.ModelFactory
	Provider    string                           // main provider name
	Providers   map[string]config.ProviderConfig // per-provider credentials
	SmallModel  string                           // model name preferred for the explore sub-agent

	// WorkspaceFS is the session's file backend, threaded into each sub-agent's
	// rebuilt read/write/edit pool so sub-agents share the parent's backend.
	WorkspaceFS agentcoretools.WorkspaceFS

	// SessionID seeds each sub-agent's prompt-cache routing key. See
	// agent.BuildDeps.SessionID.
	SessionID string
}

type subAgents struct {
	runner *subagent.Runner
	tool   *subagent.Tool
	// isolation maps agent type to "worktree" for definitions that opt into a
	// private worktree. The teammate spawner applies the cwd override.
	isolation map[string]string
}

// buildSubAgents constructs the typed runner and its model-facing adapter.
//
// The pipeline:
//
//	BuiltinDefinitions  ─┐
//	                     ├─►  MergeAgents  ─►  BuildConfig  ─►  subagent.NewRunner(cfgs...)
//	LoadAgentsDir(proj) ─┤      (later src
//	LoadAgentsDir(user) ─┘       overrides)
//
// Built-in, project (.codebot/agents/), and user (~/.codebot/agents/)
// definitions all go through the same Validate → BuildConfig path. Name
// collisions are resolved by source: user overrides project overrides
// builtin. A broken agent file does not abort startup — the loader returns
// errors per file and we log and skip them.
//
// The returned isolationOf maps each agent type to its declared isolation
// mode (only "worktree" entries are kept; shared agents are simply absent), so
// the teammate spawner can sandbox the opted-in types without re-reading defs.
func buildSubAgents(deps subAgentDeps) subAgents {
	resolveModel := buildModelResolver(deps)

	builtin := agent.BuiltinDefinitions(deps.Cwd)
	applyExploreSmallModel(builtin, deps.SmallModel)

	project, errs := agent.LoadAgentsDir(projectAgentsDir(deps.Cwd), agent.SourceProject)
	logAgentLoadErrors(errs)
	user, errs := agent.LoadAgentsDir(userAgentsDir(), agent.SourceUser)
	logAgentLoadErrors(errs)

	defs := agent.MergeAgents(builtin, project, user)

	isolationOf := make(map[string]string)
	for _, def := range defs {
		if def.Isolation == agent.WorktreeIsolation {
			isolationOf[def.Name] = def.Isolation
		}
	}

	buildDeps := agent.BuildDeps{
		Cwd:           deps.Cwd,
		MainTools:     deps.AllTools,
		DefaultModel:  deps.Model,
		ContextWindow: deps.ContextWindow,
		ResolveModel:  resolveModel,
		WorkspaceFS:   deps.WorkspaceFS,
		SessionID:     deps.SessionID,
	}
	ctxFactory := func(model agentcore.ChatModel) agentcore.ContextManager {
		return newSubAgentContextManager(model, deps.ContextWindow)
	}

	cfgs := make([]subagent.Config, 0, len(defs))
	for _, def := range defs {
		cfg, err := def.BuildConfig(buildDeps, ctxFactory)
		if err != nil {
			log.Printf("subagent %q: skipped (%v)", def.Name, err)
			continue
		}
		cfgs = append(cfgs, cfg)
	}

	runner := subagent.NewRunner(cfgs...)
	tool := runner.AsTool()

	if resolveModel != nil {
		tool.SetCreateModel(resolveModel)
	}

	return subAgents{runner: runner, tool: tool, isolation: isolationOf}
}

// applyExploreSmallModel reassigns the explore agent's Model to the
// configured small-model preset if set. We mutate in place rather than
// passing the small-model name down to BuildConfig because "small model for
// explore" is a UX preset, not a property of the explore agent's spec —
// users who write their own .codebot/agents/explore.md keep full control.
//
// No-op when SmallModel is empty or the explore definition is absent.
func applyExploreSmallModel(defs []agent.AgentDefinition, smallModel string) {
	if smallModel == "" {
		return
	}
	for i := range defs {
		if defs[i].Name == "explore" {
			defs[i].Model = smallModel
			return
		}
	}
}

// buildModelResolver returns a function that maps a model name string to a
// ChatModel, by looking through every configured provider for a model with
// that name. Returns nil when the bootstrap path has no model factory wired
// (in which case BuildConfig will refuse any non-inherit model name).
func buildModelResolver(deps subAgentDeps) func(string) (agentcore.ChatModel, error) {
	if deps.CreateModel == nil {
		return nil
	}
	factory := deps.CreateModel
	providers := deps.Providers
	defaultProv := deps.Provider
	return func(name string) (agentcore.ChatModel, error) {
		// Stop at the FIRST matching provider. A labelled break is needed
		// because map iteration over `providers` is unordered and a plain
		// `break` would only exit the inner loop — letting a later provider
		// silently overwrite the choice and producing non-deterministic
		// routing when two providers list the same model name (e.g. an
		// OpenRouter alias for an OpenAI model).
	search:
		for provName, pc := range providers {
			for _, m := range pc.Models {
				if strings.EqualFold(m, name) {
					defaultProv = provName
					break search
				}
			}
		}
		prov := defaultProv
		apiKey, baseURL := resolveFromProviders(providers, prov)
		providerExtra := resolveExtraFromProviders(providers, prov)
		provType, err := resolveProviderType(providers, prov)
		if err != nil {
			return nil, err
		}
		return factory(provType, name, apiKey, baseURL, providerExtra)
	}
}

// projectAgentsDir is the conventional location for team-shared sub-agent
// definitions. Living under the workspace means they version-control with
// the project and apply on every collaborator's machine.
func projectAgentsDir(cwd string) string {
	return filepath.Join(cwd, ".codebot", "agents")
}

// userAgentsDir is the per-machine location for personal sub-agent
// definitions. They override project definitions at MergeAgents time so a
// developer can iterate on a tweak without committing it.
func userAgentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codebot", "agents")
}

// logAgentLoadErrors emits a line per broken agent file. We log instead of
// aborting startup: one bad file in ~/.codebot/agents/ should not block the
// session.
func logAgentLoadErrors(errs []error) {
	for _, err := range errs {
		log.Printf("agent load error: %v", err)
	}
}

func newSubAgentContextManager(model agentcore.ChatModel, window int) agentcore.ContextManager {
	if model == nil || window <= 0 {
		return nil
	}
	return agentctx.NewEngine(agentctx.EngineConfig{
		ContextWindow: window,
		Strategies: []agentctx.Strategy{
			agentctx.NewToolResultMicrocompact(agentctx.ToolResultMicrocompactConfig{
				Classifier: agent.CodebotToolClassifier,
				KeepRecent: 3,
			}),
			agentctx.NewLightTrim(agentctx.LightTrimConfig{}),
			agentctx.NewFullSummary(agentctx.FullSummaryConfig{
				Model:            model,
				KeepRecentTokens: 12000,
			}),
		},
	})
}

// resolveFromProviders returns credentials for a provider from the settings
// map. Credentials come exclusively from settings.json — no env fallback.
func resolveFromProviders(providers map[string]config.ProviderConfig, prov string) (apiKey, baseURL string) {
	if pc, ok := providers[prov]; ok {
		return pc.APIKey, pc.BaseURL
	}
	return "", ""
}

func resolveExtraFromProviders(providers map[string]config.ProviderConfig, prov string) map[string]any {
	if pc, ok := providers[prov]; ok {
		return pc.ProviderExtra()
	}
	return nil
}

// resolveProviderType returns the protocol type for a provider key.
func resolveProviderType(providers map[string]config.ProviderConfig, prov string) (string, error) {
	return config.ResolveConfiguredProviderType(providers, prov)
}
