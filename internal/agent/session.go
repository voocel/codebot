package agent

import (
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/hooks"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/storage"
)

// ModelFactory creates a chat model instance for a provider/model tuple.
type ModelFactory func(prov, model, apiKey, baseURL string) (agentcore.ChatModel, error)

// SessionConfig configures a new Session.
type SessionConfig struct {
	Agent    *agentcore.Agent
	Store    *storage.Store
	Manager  *storage.Manager
	Registry *provider.ModelRegistry
	Settings config.Resolved
	Cwd      string
	// CreateModel allows tests/integrations to override model construction.
	// Defaults to provider.CreateModel when nil.
	CreateModel ModelFactory
	// LazyPersist buffers user messages and flushes them only when
	// an assistant response arrives. Disabled by default for safety.
	LazyPersist bool
	// ChatModel is the active ChatModel reference, used for max_tokens adjustment
	// during overflow recovery.
	ChatModel agentcore.ChatModel
	// HookRunner fires lifecycle hooks (notification, etc.). Nil when no hooks configured.
	HookRunner *hooks.Runner

	// Tools is the full set of tools registered with the agent.
	// Used by ToolsByName / RestoreAllTools for plan mode filtering.
	Tools []agentcore.Tool
	// ContextFiles holds loaded context files for dynamic system prompt rebuilding.
	// When tools change the prompt is regenerated from these inputs.
	ContextFiles config.ContextFiles
	// Skills holds loaded skills for system prompt injection and /skill: commands.
	Skills []config.Skill
}

// Session is the business-logic core that wraps Agent + session persistence.
// It is independent of any UI framework and drives interactive, print, and RPC modes.
type Session struct {
	agent    *agentcore.Agent
	store    *storage.Store
	mgr      *storage.Manager
	registry *provider.ModelRegistry
	settings config.Resolved

	provider  string
	modelName string
	providers map[string]config.ProviderConfig
	cwd       string

	createModel ModelFactory

	allTools     []agentcore.Tool
	activeTools  []agentcore.Tool
	contextFiles config.ContextFiles
	skills       []config.Skill
	suffix       string
	beforePrompt func()
	hookRunner   *hooks.Runner

	listeners []func(SessionEvent)
	unsub     func()

	retryAttempt     int
	overflowDetected bool
	overflowErr      error

	chatModel         agentcore.ChatModel
	maxTokensReduced  bool
	originalMaxTokens int

	lazyPersist    bool
	pendingUserMsg []agentcore.Message
	autoNamed      bool

	prompts     *sessionPromptManager
	persistence *sessionPersistence
	context     *sessionContextController

	mu sync.Mutex
}

// NewSession creates a Session and wires auto-persist to the agent.
func NewSession(cfg SessionConfig) *Session {
	modelFactory := cfg.CreateModel
	if modelFactory == nil {
		modelFactory = provider.CreateModel
	}

	s := &Session{
		agent:        cfg.Agent,
		store:        cfg.Store,
		mgr:          cfg.Manager,
		registry:     cfg.Registry,
		settings:     cfg.Settings,
		provider:     cfg.Settings.Provider,
		modelName:    cfg.Settings.Model,
		providers:    cfg.Settings.Providers,
		cwd:          cfg.Cwd,
		createModel:  modelFactory,
		lazyPersist:    cfg.LazyPersist,
		chatModel:      cfg.ChatModel,
		hookRunner:   cfg.HookRunner,
		allTools:     cfg.Tools,
		activeTools:  cfg.Tools,
		contextFiles: cfg.ContextFiles,
		skills:       cfg.Skills,
	}

	s.prompts = newSessionPromptManager(s)
	s.persistence = newSessionPersistence(s)
	s.context = newSessionContextController(s)

	s.unsub = cfg.Agent.Subscribe(s.handleAgentEvent)
	return s
}
