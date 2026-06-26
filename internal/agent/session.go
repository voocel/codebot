package agent

import (
	"sort"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	agenttools "github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/goal"
	"github.com/voocel/codebot/internal/hooks"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/telemetry"
)

// ModelFactory creates a chat model instance for a provider/model tuple.
type ModelFactory func(prov, model, apiKey, baseURL string, providerExtra map[string]any) (agentcore.ChatModel, error)

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
	// TelemetryTracer opens agent-run spans and tool spans when telemetry is enabled.
	TelemetryTracer *telemetry.Tracer
	// HookRunner fires lifecycle hooks (notification, etc.). Nil when no hooks configured.
	HookRunner *hooks.Runner
	// Snapshotter records workspace file checkpoints at turn boundaries and
	// powers /undo. Nil disables snapshotting (e.g. outside a git repo).
	Snapshotter Snapshotter

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

	// FileReadState records read timestamps consumed by write/edit Validators.
	// Held on the Session so Clear / Reset / SwitchSession can drop stale
	// stamps — otherwise the LLM may write based on stamps from a read it
	// no longer has in its conversation history.
	FileReadState *agenttools.FileReadState

	// FrozenIdentity / FrozenInstructions are the process-stable parts of the
	// system prompt (block 1 + block 2). Computed once at assembly time and
	// reused on every rebuild — never recomputed during the session.
	// See config.BuildFrozenSystemParts.
	FrozenIdentity     string
	FrozenInstructions string
	// InitialMCPOverlay seeds the mcp overlay when the assembly already knows
	// the MCP instructions (e.g. MCP managers that connect synchronously).
	// Written directly into the overlay store without triggering a rebuild.
	InitialMCPOverlay string

	// InitialDynamic is the assembly-time dynamic block text (MCP tool list +
	// overlays). Stored on the session so teammate spawn can read the current
	// snapshot via DynamicSystemBlock() without re-computing. Updated when
	// rebuildPrompt runs (overlay change / MCP refresh).
	InitialDynamic string
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
	planModeSignal    func() PlanModeSignal
	goalSignal        func() goal.Signal
	goalUsageLimit    func(string) (goal.State, error)
	hookRunner        *hooks.Runner
	snapshotter       Snapshotter
	taskStore         *storage.TaskStore
	skillAllowsSetter func([]string)
	skillRuntime      skillRuntimeState
	fileReadState     *agenttools.FileReadState

	frozenIdentity     string // block 1 — process-stable, never recomputed
	frozenInstructions string // block 2 — process-stable, never recomputed
	dynamicText        string // block 3 — refreshed by rebuildPrompt; read by teammate spawn

	deferredToolsPreamble string // <available-deferred-tools> for first user message
	reminders             reminderState
	preambleInjected      bool // true after first preamble injection

	listeners []func(SessionEvent)
	unsub     func()

	retryAttempt int

	chatModel       agentcore.ChatModel
	telemetryTracer *telemetry.Tracer
	activeRun       *telemetry.Run

	lazyPersist        bool
	pendingUserMsg     []agentcore.Message
	autoNamed          bool
	lastAssistantStart time.Time          // set at EventMessageStart (assistant), consumed at EventMessageEnd for latency_ms
	cacheSnap          cacheSnapshot      // previous turn's system/tools fingerprint + cache_read, updated after every LLM call
	sessionMemory      sessionMemoryState // background extraction bookkeeping — see session_memory.go
	pendingToolCalls   map[string]pendingToolCall
	recentToolCalls    []toolCallFingerprint
	currentTurn        TurnOutcomeSnapshot
	lastTurn           TurnOutcomeSnapshot
	lastRunSummary     *agentcore.RunSummary
	lastReminder       *ReminderSnapshot
	lastCompaction     *CompactionSnapshot
	recentErrors       []ErrorSnapshot
	dirtySeq           uint64 // incremented each time a repo-mutating tool succeeds; hook goroutine captures this and only clears if unchanged
	generation         uint64 // incremented on session switch; async goroutines check this to avoid cross-session callbacks

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

// reminderState bundles all reminder bookkeeping for a Session.
//
//   - static: persistent across turns (context-file derived), never cleared
//   - runtime: one-shot queue consumed on the next user prompt
//   - runtimeKeys: dedup for the queue
//   - steeredKeys / autoResumeKeys: per-turn dedup for the two in-flight
//     delivery paths (Steer / Inject) — reset at turn start and agent end
//   - pendingContinue: marks that a Steer'd reminder needs a Continue() call
//     after the current run finishes
type reminderState struct {
	static          []string
	runtime         []string
	runtimeKeys     map[string]struct{}
	steeredKeys     map[string]struct{}
	autoResumeKeys  map[string]struct{}
	pendingContinue bool
}

func newReminderState(static []string) reminderState {
	return reminderState{
		static:         static,
		runtimeKeys:    make(map[string]struct{}),
		steeredKeys:    make(map[string]struct{}),
		autoResumeKeys: make(map[string]struct{}),
	}
}

// resetAll clears every transient field. 'static' is preserved.
// Used by Session.resetHarnessStateLocked on session reset/switch.
func (r *reminderState) resetAll() {
	r.runtime = nil
	r.runtimeKeys = make(map[string]struct{})
	r.steeredKeys = make(map[string]struct{})
	r.autoResumeKeys = make(map[string]struct{})
	r.pendingContinue = false
}

// resetTurnDelivery clears per-turn delivery dedup state. The runtime queue
// is preserved (queued reminders fire on the next user prompt).
// Used by Session.beginTurn at the start of each turn.
func (r *reminderState) resetTurnDelivery() {
	r.steeredKeys = make(map[string]struct{})
	r.autoResumeKeys = make(map[string]struct{})
	r.pendingContinue = false
}

// overlayStore holds named instructions overlays appended to the dynamic
// system block. Output is sorted by key so the byte sequence is stable
// across rebuilds — insertion order would let plan-mode enter/exit cycles
// or MCP reconnects shuffle the hash without changing meaning.
type overlayStore struct {
	byKey map[string]string
}

func (o *overlayStore) set(key, text string) {
	if o.byKey == nil {
		o.byKey = make(map[string]string)
	}
	if text == "" {
		delete(o.byKey, key)
		return
	}
	o.byKey[key] = text
}

func (o *overlayStore) texts() []string {
	if len(o.byKey) == 0 {
		return nil
	}
	keys := make([]string, 0, len(o.byKey))
	for k := range o.byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, o.byKey[k])
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
		telemetryTracer:   cfg.TelemetryTracer,
		hookRunner:        cfg.HookRunner,
		snapshotter:       cfg.Snapshotter,
		taskStore:         cfg.TaskStore,
		allTools:          cfg.Tools,
		activeTools:       cfg.Tools,
		contextFiles:      cfg.ContextFiles,
		skills:            cfg.Skills,
		skillCatalog:      cfg.SkillCatalog,
		skillUsage:        cfg.SkillUsage,
		skillAllowsSetter: cfg.SkillAllowsSetter,
		fileReadState:     cfg.FileReadState,

		frozenIdentity:     cfg.FrozenIdentity,
		frozenInstructions: cfg.FrozenInstructions,
		dynamicText:        cfg.InitialDynamic,

		deferredToolsPreamble: cfg.DeferredToolsPreamble,
		reminders:             newReminderState(cfg.Reminders),
		pendingToolCalls:      make(map[string]pendingToolCall),
		preambleInjected:      cfg.PreambleInjected,
	}
	if cfg.InitialMCPOverlay != "" {
		s.overlays.set("mcp", cfg.InitialMCPOverlay)
	}

	s.prompts = newSessionPromptManager(s)
	s.persistence = newSessionPersistence(s)
	s.context = newSessionContextController(s)
	s.runtime = newSessionRuntimePolicy(s)
	s.metrics = newRuntimeMetrics()

	s.unsub = cfg.Agent.Subscribe(s.handleAgentEvent)
	return s
}

// DynamicSystemBlock returns the current dynamic block snapshot (MCP tool
// inventory + active overlays), or nil when there's nothing to include.
//
// Called by the teammate spawner at spawn time so teammates inherit the
// leader's MCP descriptions and overlay state. Each teammate freezes its
// system prompt at spawn — later leader-side changes (plan-mode toggle /
// MCP refresh) do NOT propagate to already-running teammates.
//
// The block intentionally carries no CacheControl. Two reasons:
//  1. Anthropic caps system-field cache_control markers; the universal base
//     and role block already consume two — leaving the dynamic block uncached
//     keeps headroom for the message-level marker the agent loop adds.
//  2. The dynamic block can churn mid-session (plan-mode toggle, MCP
//     reconnect); a cache breakpoint here would be invalidated frequently and
//     would charge cache-write cost for short-lived content.
func (s *Session) DynamicSystemBlock() *agentcore.SystemBlock {
	s.mu.Lock()
	text := s.dynamicText
	s.mu.Unlock()
	if text == "" {
		return nil
	}
	return &agentcore.SystemBlock{Text: text}
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
	s.reminders.resetAll()
	s.pendingToolCalls = make(map[string]pendingToolCall)
	s.recentToolCalls = nil
	s.currentTurn = TurnOutcomeSnapshot{}
	s.lastTurn = TurnOutcomeSnapshot{}
	s.lastRunSummary = nil
	s.lastReminder = nil
	s.lastCompaction = nil
	s.recentErrors = nil
	s.dirtySeq = 0
	s.metrics = newRuntimeMetrics()
	s.skillRuntime = skillRuntimeState{}
	// Drop file-read stamps along with the rest of the harness state. After
	// a reset the LLM has no read history; keeping stamps would let write/edit
	// validate against reads the LLM no longer remembers.
	if s.fileReadState != nil {
		s.fileReadState.Reset()
	}
	// Repoint at the new session's persisted undo stack (empty for a fresh
	// session); prior snapshots no longer map to this conversation.
	if s.snapshotter != nil {
		s.snapshotter.Rebind(s.undoStatePath())
	}
}
