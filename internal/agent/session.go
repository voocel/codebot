package agent

import (
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/hooks"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/storage"
)

// ModelFactory creates a chat model instance for a provider/model tuple.
type ModelFactory func(prov, model, apiKey, baseURL string) (agentcore.ChatModel, error)

// SessionConfig configures a new Session.
type SessionConfig struct {
	Agent          *agentcore.Agent
	ContextManager agentcore.ContextManager
	Store          *storage.Store
	Manager        *storage.Manager
	Registry       *provider.ModelRegistry
	Settings       config.Resolved
	Cwd            string
	TaskStore      *storage.TaskStore
	// CreateModel allows tests/integrations to override model construction.
	// Defaults to provider.CreateModel when nil.
	CreateModel ModelFactory
	// LazyPersist buffers user messages and flushes them only when
	// an assistant response arrives. Disabled by default for safety.
	LazyPersist bool
	// ChatModel is the active ChatModel reference.
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
	Skills []skill.Spec
	// SkillCatalog provides indexed skill lookup and reload support.
	SkillCatalog *skill.Catalog
	// SkillUsage persists cross-session usage statistics for prompt ordering.
	SkillUsage *skill.UsageTracker
	// DeferredToolsPreamble is injected as the first user message (once).
	DeferredToolsPreamble string
	// Reminders are <system-reminder> fragments prepended to each user message.
	Reminders []string
	// PreambleInjected indicates the preamble was already in conversation history (resume).
	PreambleInjected bool
	// SkillAllowsSetter updates temporary tool allows for the active skill.
	SkillAllowsSetter func([]string)
}

// Session is the business-logic core that wraps Agent + session persistence.
// It is independent of any UI framework and drives interactive, print, and RPC modes.
type Session struct {
	agent          *agentcore.Agent
	contextManager agentcore.ContextManager
	store          *storage.Store
	mgr            *storage.Manager
	registry       *provider.ModelRegistry
	settings       config.Resolved

	provider  string
	modelName string
	providers map[string]config.ProviderConfig
	cwd       string

	createModel ModelFactory

	allTools          []agentcore.Tool
	activeTools       []agentcore.Tool
	contextFiles      config.ContextFiles
	skills            []skill.Spec
	skillCatalog      *skill.Catalog
	skillUsage        *skill.UsageTracker
	overlays          overlayStore
	beforePrompt      func()
	planModeSignal    func() (active bool, planFilePath string)
	hookRunner        *hooks.Runner
	taskStore         *storage.TaskStore
	skillAllowsSetter func([]string)
	skillRuntime      skillRuntimeState

	deferredToolsPreamble  string   // <available-deferred-tools> for first user message
	staticReminders        []string // 从上下文文件生成的稳定 reminders
	runtimeReminders       []string // 运行时动态注入的一次性 reminders
	runtimeReminderKeys    map[string]struct{}
	steeredReminderKeys    map[string]struct{}
	autoResumeReminderKeys map[string]struct{}
	preambleInjected       bool // true after first preamble injection

	listeners []func(SessionEvent)
	unsub     func()

	retryAttempt int

	chatModel agentcore.ChatModel

	lazyPersist             bool
	pendingUserMsg          []agentcore.Message
	autoNamed               bool
	lastAssistantStart      time.Time          // set at EventMessageStart (assistant), consumed at EventMessageEnd for latency_ms
	cacheSnap               cacheSnapshot      // previous turn's system/tools fingerprint + cache_read, updated after every LLM call
	sessionMemory           sessionMemoryState // background extraction bookkeeping — see session_memory.go
	pendingToolCalls        map[string]pendingToolCall
	recentToolCalls         []toolCallFingerprint
	pendingReminderContinue bool
	currentTurn             TurnOutcomeSnapshot
	lastTurn                TurnOutcomeSnapshot
	lastRunSummary          *agentcore.RunSummary
	lastReminder            *ReminderSnapshot
	lastCompaction          *CompactionSnapshot
	recentErrors            []ErrorSnapshot
	dirtySeq                uint64 // incremented each time a repo-mutating tool succeeds; hook goroutine captures this and only clears if unchanged
	generation              uint64 // incremented on session switch; async goroutines check this to avoid cross-session callbacks

	prompts     *sessionPromptManager
	persistence *sessionPersistence
	context     *sessionContextController
	runtime     *sessionRuntimePolicy
	metrics     *runtimeMetrics

	mu sync.Mutex
}

type skillRuntimeState struct {
	active          bool
	baseProvider    string
	baseModel       string
	baseChatModel   agentcore.ChatModel
	baseThinking    string
	invoked         []invokedSkillSnapshot
	invocationCount map[string]int
}

type invokedSkillSnapshot struct {
	Name       string
	PromptText string
	Paths      []string
	Timestamp  time.Time
}

type overlayStore struct {
	order []string
	byKey map[string]string
}

func (o *overlayStore) set(key, text string) {
	if o.byKey == nil {
		o.byKey = make(map[string]string)
	}
	if text == "" {
		delete(o.byKey, key)
		for i, k := range o.order {
			if k == key {
				o.order = append(o.order[:i], o.order[i+1:]...)
				break
			}
		}
		return
	}
	if _, exists := o.byKey[key]; !exists {
		o.order = append(o.order, key)
	}
	o.byKey[key] = text
}

func (o *overlayStore) texts() []string {
	out := make([]string, 0, len(o.order))
	for _, k := range o.order {
		if v := o.byKey[k]; v != "" {
			out = append(out, v)
		}
	}
	return out
}

// NewSession creates a Session and wires auto-persist to the agent.
func NewSession(cfg SessionConfig) *Session {
	modelFactory := cfg.CreateModel
	if modelFactory == nil {
		modelFactory = provider.CreateModel
	}

	s := &Session{
		agent:             cfg.Agent,
		contextManager:    cfg.ContextManager,
		store:             cfg.Store,
		mgr:               cfg.Manager,
		registry:          cfg.Registry,
		settings:          cfg.Settings,
		provider:          cfg.Settings.Provider,
		modelName:         cfg.Settings.Model,
		providers:         cfg.Settings.Providers,
		cwd:               cfg.Cwd,
		createModel:       modelFactory,
		lazyPersist:       cfg.LazyPersist,
		chatModel:         cfg.ChatModel,
		hookRunner:        cfg.HookRunner,
		taskStore:         cfg.TaskStore,
		allTools:          cfg.Tools,
		activeTools:       cfg.Tools,
		contextFiles:      cfg.ContextFiles,
		skills:            cfg.Skills,
		skillCatalog:      cfg.SkillCatalog,
		skillUsage:        cfg.SkillUsage,
		skillAllowsSetter: cfg.SkillAllowsSetter,

		deferredToolsPreamble:  cfg.DeferredToolsPreamble,
		staticReminders:        cfg.Reminders,
		runtimeReminderKeys:    make(map[string]struct{}),
		steeredReminderKeys:    make(map[string]struct{}),
		autoResumeReminderKeys: make(map[string]struct{}),
		pendingToolCalls:       make(map[string]pendingToolCall),
		preambleInjected:       cfg.PreambleInjected,
	}

	s.prompts = newSessionPromptManager(s)
	s.persistence = newSessionPersistence(s)
	s.context = newSessionContextController(s)
	s.runtime = newSessionRuntimePolicy(s)
	s.metrics = newRuntimeMetrics()

	s.unsub = cfg.Agent.Subscribe(s.handleAgentEvent)
	return s
}

func (s *Session) SetTaskNotifyFn(fn storage.TaskNotifyFn) {
	if s.taskStore == nil {
		return
	}
	s.taskStore.SetNotifyFn(fn)
}

func (s *Session) TaskSnapshot() storage.TaskSnapshot {
	if s.taskStore == nil {
		return storage.TaskSnapshot{}
	}
	return s.taskStore.Snapshot()
}

func (s *Session) ResetTaskList() error {
	if s.taskStore == nil {
		return nil
	}
	return s.taskStore.Reset()
}

func (s *Session) resetHarnessStateLocked() {
	s.generation++
	s.runtimeReminders = nil
	s.runtimeReminderKeys = make(map[string]struct{})
	s.steeredReminderKeys = make(map[string]struct{})
	s.autoResumeReminderKeys = make(map[string]struct{})
	s.pendingToolCalls = make(map[string]pendingToolCall)
	s.recentToolCalls = nil
	s.pendingReminderContinue = false
	s.currentTurn = TurnOutcomeSnapshot{}
	s.lastTurn = TurnOutcomeSnapshot{}
	s.lastRunSummary = nil
	s.lastReminder = nil
	s.lastCompaction = nil
	s.recentErrors = nil
	s.dirtySeq = 0
	s.metrics = newRuntimeMetrics()
	s.skillRuntime = skillRuntimeState{}
}
