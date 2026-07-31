package agent

import (
	"maps"
	"sort"
	"sync"
	"sync/atomic"
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
	// ContextFiles holds the workspace context rendered into system block 2.
	// Session.Reload re-reads it from disk and rebuilds that block; nothing
	// else in the session mutates it.
	ContextFiles config.ContextFiles
	// Skills holds loaded skills for system prompt injection and /skill: commands.
	Skills []skill.Spec
	// SkillCatalog provides indexed skill lookup and reload support.
	SkillCatalog *skill.Catalog
	// SkillUsage persists cross-session usage statistics for prompt ordering.
	SkillUsage *skill.UsageTracker
	// DeferredToolsPreamble is injected as the first user message (once).
	DeferredToolsPreamble string
	// SkillAllowsSetter updates temporary tool allows for the active skill.
	SkillAllowsSetter func([]string)

	// FileReadState records read timestamps consumed by write/edit Validators.
	// Held on the Session so Clear / Reset / SwitchSession can drop stale
	// stamps — otherwise the LLM may write based on stamps from a read it
	// no longer has in its conversation history.
	FileReadState *agenttools.FileReadState

	// FrozenIdentity is system block 1's assembly-time value; a worktree
	// retarget recomputes it for the new root. FrozenInstructions is block 2's;
	// a Reload or retarget recomputes it from ContextFiles + Skills +
	// LocalTools. See config.BuildFrozenSystemParts.
	FrozenIdentity     string
	FrozenInstructions string
	// LocalTools is the session-stable local tool inventory as rendered into
	// block 2. Held so a reload can rebuild that block without re-deriving it
	// from the live tool set, which plan mode filters.
	LocalTools []config.ToolInfo
	// InitialMCPOverlay seeds the mcp overlay when the assembly already knows
	// the MCP instructions (e.g. MCP managers that connect synchronously).
	// Written directly into the overlay store without triggering a rebuild.
	InitialMCPOverlay string

	// InitialDynamic is the assembly-time dynamic block text (MCP tool list +
	// overlays). Stored on the session so teammate spawn can read the current
	// snapshot via DynamicSystemBlock() without re-computing. Updated when
	// the prompt rebuilds (overlay change / MCP refresh).
	InitialDynamic string
}

// sessionDeps is what assembly binds once and never reassigns. Grouping them
// makes "no lock needed" a property of the type rather than a rule every reader
// has to re-derive. Anything mutable belongs on Session proper or in a
// component (cwd, reassigned by /worktree, lives on Session as an atomic).
type sessionDeps struct {
	agent          *agentcore.Agent
	contextManager agentcore.ContextManager
	mgr            *storage.Manager
	registry       *provider.ModelRegistry
	createModel    ModelFactory
	lazyPersist    bool

	telemetryTracer *telemetry.Tracer
	hookRunner      *hooks.Runner
	snapshotter     Snapshotter
	taskStore       *storage.TaskStore
	fileReadState   *agenttools.FileReadState

	// providers is settings.Providers — the same map, never mutated after
	// boot (only read for credential/type/small-model lookups), so it needs
	// no lock and no copy.
	providers map[string]config.ProviderConfig
	// unsub detaches handleAgentEvent from the agent; set once in NewSession,
	// called once in Close.
	unsub func()
}

// sessionHooks are the callbacks bootstrap wiring injects after construction.
// It guards itself: wiring is NOT one-shot — plugin hot reload re-runs
// WireGoalTools from the UI goroutine while the agent goroutine reads
// idleHook / skillAllowsSetter, so an "immutable after boot" assumption
// does not hold. Callbacks are invoked outside the lock.
type sessionHooks struct {
	mu sync.Mutex

	beforePrompt      func()
	idleHook          func()
	planModeSignal    func() PlanModeSignal
	goalSignal        func() goal.Signal
	goalUsageLimit    func(string) (goal.State, error)
	skillAllowsSetter func([]string)
}

func (h *sessionHooks) setBeforePrompt(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beforePrompt = fn
}

func (h *sessionHooks) setIdleHook(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.idleHook = fn
}

func (h *sessionHooks) setPlanModeSignal(fn func() PlanModeSignal) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.planModeSignal = fn
}

func (h *sessionHooks) setGoalSignal(fn func() goal.Signal) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.goalSignal = fn
}

func (h *sessionHooks) setGoalUsageLimit(fn func(string) (goal.State, error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.goalUsageLimit = fn
}

func (h *sessionHooks) getBeforePrompt() func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.beforePrompt
}

func (h *sessionHooks) getIdleHook() func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.idleHook
}

func (h *sessionHooks) getPlanModeSignal() func() PlanModeSignal {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.planModeSignal
}

func (h *sessionHooks) getGoalSignal() func() goal.Signal {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.goalSignal
}

func (h *sessionHooks) getGoalUsageLimit() func(string) (goal.State, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.goalUsageLimit
}

func (h *sessionHooks) getSkillAllows() func([]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.skillAllowsSetter
}

// Session is the business-logic core that wraps Agent + session persistence.
// It is independent of any UI framework and drives interactive, print, and RPC modes.
//
// State is grouped into self-guarded components — each owns its fields, its
// lock, and its reset; none of their methods reference Session (statically
// checkable by grepping the *_state.go method bodies), so component locks are
// leaves and no lock cycle can form. Nesting directions that do occur:
//
//	s.mu → run.mu                     (continueIfCurrentGeneration holds s.mu
//	                                   through beginTelemetryRun / rollback)
//	s.mu → agent lock                 (generation check + commit in one
//	                                   critical section: run launch in
//	                                   continueIfCurrentGeneration, SetMessages
//	                                   in the compaction commit; acyclic
//	                                   because the agent never calls back into
//	                                   the Session under its own lock —
//	                                   listener dispatch and the
//	                                   MessageCommitter run outside it, per the
//	                                   Subscribe lifecycle contract)
//	prompt.mu / model.mu → agent lock (sink delivery — covered by the setters'
//	                                   documented purity contract: pure
//	                                   assignment, no events, no callbacks),
//	                                   and prompt.mu → cache.mu (leaf delivery
//	                                   target)
//	flushMu → persist.mu              (lazy-persist drain)
//
// s.mu itself guards generation and backgroundWakeSuppressed. Session-identity
// surgery (Reset / SwitchSession / ClearConversation) serializes with switchMu
// and freezes the run lifecycle itself with agent.HoldRuns — continuation
// launches then fail fast with ErrRunsHeld inside the kernel instead of
// coordinating through a session-side lock. Accepted cross-group snapshot
// windows are commented at their sites (persistLLMCall, ephemeralQuery, the
// dirtySeq/generation CAS in runPostStopValidation).
type Session struct {
	deps  sessionDeps  // assembly-time, immutable — no lock
	hooks sessionHooks // self-guarded — see type doc

	// cwd is the live workspace root, rewritten by /worktree enter/exit
	// (RetargetWorkspace) and read on every tool call via baseRunCtx —
	// atomic so the hot path never contends on a lock.
	cwd atomic.Pointer[string]

	model   modelState   // model identity + settings + skill-override baseline; owns its own lock
	persist persistState // session-log store + lazy-persist bookkeeping; owns its own lock
	prompt  *promptState // tool surface + system-prompt inputs + skill invocation log; owns its own lock
	run     runState     // active telemetry span + retry attempt; owns its own lock

	reminders reminderState

	events eventBus // SessionEvent fan-out; owns its own lock

	cache         cacheMonitor       // previous turn's system/tools fingerprint + cache_read; owns its own lock
	sessionMemory sessionMemoryState // background extraction bookkeeping — see session_memory.go
	turn          turnState          // tool-call tracking + turn outcome + run summary; owns its own lock
	generation    uint64             // incremented on session switch; async goroutines check this to avoid cross-session callbacks

	backgroundWakeSuppressed bool // explicit cancellation keeps results queued without restarting the agent

	persistence *sessionPersistence
	runtime     *sessionRuntimePolicy
	metrics     runtimeMetrics // diagnostic counters + error ring; owns its own lock

	// switchMu serializes the HoldRuns holders (Reset / SwitchSession /
	// ClearConversation) against each other. The run lifecycle itself is
	// frozen by agent.HoldRuns — a count, not a mutex, so without this two
	// holders' teardowns would interleave their surgery.
	switchMu sync.Mutex

	mu sync.Mutex
}

type invokedSkillSnapshot struct {
	Name       string
	PromptText string
	Paths      []string
	Timestamp  time.Time
}

// reminderState owns all reminder bookkeeping for a Session, together with the
// lock guarding it. Callers go through the methods below — the fields are never
// touched directly, so a new delivery path cannot forget to take the lock.
//
//   - runtime: one-shot queue consumed on the next user prompt
//   - runtimeKeys: dedup for the queue
//   - lastDate: the calendar date baked into system block 1. Only a session
//     that outlives midnight needs a correction, and then exactly one.
//   - steeredKeys / autoResumeKeys: per-turn dedup for the two in-flight
//     delivery paths (Steer / Inject) — reset at turn start and agent end
//   - pendingContinue: marks that a Steer'd reminder needs a Continue() call
//     after the current run finishes
//   - recallSurfaced / recallBytes: auto-memory recall accounting. Recall is
//     reminder injection with a budget, and it resets on exactly the same
//     events, so it lives here rather than as two loose Session fields.
//
// The zero value is usable.
type reminderState struct {
	mu sync.Mutex

	runtime         []string
	runtimeKeys     map[string]struct{}
	steeredKeys     map[string]struct{}
	autoResumeKeys  map[string]struct{}
	pendingContinue bool
	lastDate        string

	recallSurfaced map[string]bool
	recallBytes    int
}

// drainForPrompt reads and clears the one-shot queue in one critical section:
// splitting them would drop a reminder queued between the two steps.
func (r *reminderState) drainForPrompt() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	runtime := r.runtime
	r.runtime = nil
	r.runtimeKeys = nil
	return runtime
}

// takeDateChange reports whether today differs from the date currently stated
// in system block 1, and records the new value so the correction is sent once.
// The baseline is seeded from config.SessionDate at construction rather than
// from the first prompt: a process started at 23:59 whose first prompt lands at
// 00:01 must still correct itself.
func (r *reminderState) takeDateChange(today string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastDate == today {
		return false
	}
	r.lastDate = today
	return true
}

// queue reports false when the key was already queued this context window.
func (r *reminderState) queue(key, reminder string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.runtimeKeys[key]; exists {
		return false
	}
	if r.runtimeKeys == nil {
		r.runtimeKeys = make(map[string]struct{})
	}
	r.runtimeKeys[key] = struct{}{}
	r.runtime = append(r.runtime, reminder)
	return true
}

func (r *reminderState) hasQueued() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runtime) > 0
}

// markSteered also flags that a Continue() is owed once the run finishes.
// Reports false when the key was already steered this turn.
func (r *reminderState) markSteered(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.steeredKeys[key]; exists {
		return false
	}
	if r.steeredKeys == nil {
		r.steeredKeys = make(map[string]struct{})
	}
	r.steeredKeys[key] = struct{}{}
	r.pendingContinue = true
	return true
}

func (r *reminderState) autoResumed(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.autoResumeKeys[key]
	return exists
}

func (r *reminderState) markAutoResumed(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.autoResumeKeys == nil {
		r.autoResumeKeys = make(map[string]struct{})
	}
	r.autoResumeKeys[key] = struct{}{}
}

func (r *reminderState) takePendingContinue() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := r.pendingContinue
	r.pendingContinue = false
	return pending
}

func (r *reminderState) clearPendingContinue() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingContinue = false
}

// resetAll drops every queue and dedup set, including recall accounting.
//
// lastDate rewinds to what block 1 states: it tracks the date the model
// currently believes, and a wipe destroys the rollover correction while
// leaving block 1 stale — so the correction must be re-sent.
func (r *reminderState) resetAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runtime = nil
	r.runtimeKeys = nil
	r.steeredKeys = nil
	r.autoResumeKeys = nil
	r.pendingContinue = false
	r.recallSurfaced = nil
	r.recallBytes = 0
	r.lastDate = config.SessionDate()
}

// resetTurnDelivery runs at turn start. The runtime queue survives — queued
// reminders fire on the next user prompt.
func (r *reminderState) resetTurnDelivery() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steeredKeys = nil
	r.autoResumeKeys = nil
	r.pendingContinue = false
}

// clearDeliveryDedup runs at turn end. Unlike resetTurnDelivery it keeps
// pendingContinue: the Continue() owed by a Steer'd reminder is consumed after
// the run ends, so clearing it here would swallow it.
func (r *reminderState) clearDeliveryDedup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steeredKeys = nil
	r.autoResumeKeys = nil
}

func (r *reminderState) recallBudget() (exclude map[string]bool, budgetLeft int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	exclude = make(map[string]bool, len(r.recallSurfaced))
	maps.Copy(exclude, r.recallSurfaced)
	return exclude, memoryRecallSessionBytes - r.recallBytes
}

// chargeRecall reports false when the budget is spent and the caller must stop.
func (r *reminderState) chargeRecall(path string, size int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recallBytes >= memoryRecallSessionBytes {
		return false
	}
	if r.recallSurfaced == nil {
		r.recallSurfaced = make(map[string]bool)
	}
	r.recallSurfaced[path] = true
	r.recallBytes += size
	return true
}

// resetSummarized drops what a compaction summarized away: recalled memories
// and the date correction. Narrower than resetAll on purpose — compaction
// keeps the tail, so the per-turn delivery sets there must survive.
func (r *reminderState) resetSummarized() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recallSurfaced = nil
	r.recallBytes = 0
	r.lastDate = config.SessionDate()
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
		deps: sessionDeps{
			agent:           cfg.Agent,
			contextManager:  cfg.ContextManager,
			mgr:             cfg.Manager,
			registry:        cfg.Registry,
			createModel:     modelFactory,
			lazyPersist:     cfg.LazyPersist,
			telemetryTracer: cfg.TelemetryTracer,
			hookRunner:      cfg.HookRunner,
			snapshotter:     cfg.Snapshotter,
			taskStore:       cfg.TaskStore,
			fileReadState:   cfg.FileReadState,
			providers:       cfg.Settings.Providers,
		},
		hooks: sessionHooks{skillAllowsSetter: cfg.SkillAllowsSetter},
		model: modelState{
			sink:      cfg.Agent,
			provider:  cfg.Settings.Provider,
			modelName: cfg.Settings.Model,
			chatModel: cfg.ChatModel,
			settings:  cfg.Settings,
		},

		persist: persistState{store: cfg.Store},

		// Baseline is what system block 1 actually says, not "now".
		reminders: reminderState{lastDate: config.SessionDate()},
	}
	cwd := cfg.Cwd
	s.cwd.Store(&cwd)
	// prompt needs pointers to sibling components (reminders/cache), so it is
	// wired after the Session literal. Still single-threaded here.
	s.prompt = &promptState{
		sink:                  cfg.Agent,
		cache:                 &s.cache,
		usage:                 cfg.SkillUsage,
		frozenIdentity:        cfg.FrozenIdentity,
		frozenInstructions:    cfg.FrozenInstructions,
		localToolInfos:        cfg.LocalTools,
		allTools:              cfg.Tools,
		activeTools:           cfg.Tools,
		contextFiles:          cfg.ContextFiles,
		skills:                cfg.Skills,
		skillCatalog:          cfg.SkillCatalog,
		dynamicText:           cfg.InitialDynamic,
		deferredToolsPreamble: cfg.DeferredToolsPreamble,
	}
	if cfg.InitialMCPOverlay != "" {
		s.prompt.overlays.set("mcp", cfg.InitialMCPOverlay)
	}

	s.persistence = newSessionPersistence(s)
	s.runtime = newSessionRuntimePolicy(s)

	cfg.Agent.SetMessageCommitter(s.persistence.handleCommittedMessage)
	s.deps.unsub = cfg.Agent.Subscribe(s.handleAgentEvent)
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
//
// IdentitySystemBlock returns the leader current block 1 for a teammate to
// build on. Call it per spawn — a worktree retarget rewrites this block.
func (s *Session) IdentitySystemBlock() []agentcore.SystemBlock {
	text := s.prompt.identitySnapshot()
	if text == "" {
		return nil
	}
	return []agentcore.SystemBlock{{Text: text, CacheControl: "ephemeral"}}
}

func (s *Session) DynamicSystemBlock() *agentcore.SystemBlock {
	text := s.prompt.dynamicSnapshot()
	if text == "" {
		return nil
	}
	return &agentcore.SystemBlock{Text: text}
}

func (s *Session) SetTaskNotifyFn(fn storage.TaskNotifyFn) {
	if s.deps.taskStore == nil {
		return
	}
	s.deps.taskStore.SetNotifyFn(fn)
}

func (s *Session) TaskSnapshot() storage.TaskSnapshot {
	if s.deps.taskStore == nil {
		return storage.TaskSnapshot{}
	}
	return s.deps.taskStore.Snapshot()
}

func (s *Session) ResetTaskList() error {
	if s.deps.taskStore == nil {
		return nil
	}
	return s.deps.taskStore.Reset()
}

// resetContextWindowState drops everything whose validity is derived from the
// current conversation. Any path that replaces the message history must call
// exactly this — /clear, Reset, SwitchSession — so a new piece of
// history-derived state is added in one place instead of three.
//
// State that is derived from the TRANSCRIPT rather than held in a field does
// not belong here: it heals itself when the history changes (see
// Session.pendingPreamble). Prefer that shape; this list is for state that
// genuinely cannot be recomputed.
//
// Runs OUTSIDE s.mu — every component self-locks.
func (s *Session) resetContextWindowState() {
	// Runtime reminder queue + auto-memory recall accounting: both describe
	// what the vanished history already carried.
	s.reminders.resetAll()
	// The extraction watermark belongs to the old history: keeping it would
	// suppress memory extraction until the new history outgrows the old one.
	s.sessionMemory.reset()
	// The first cache_read after a history swap has no meaningful baseline —
	// invalidate so detectCacheBreak skips one observation instead of
	// reporting a phantom break.
	s.cache.invalidateBaseline()
	// Without a read history, keeping file-read stamps would let write/edit
	// validate against reads the LLM no longer remembers.
	if s.deps.fileReadState != nil {
		s.deps.fileReadState.Reset()
	}
}

// resetHarnessState is the switch-level reset contract: the context-window
// state above PLUS everything tied to session identity (persist is handled by
// swapStore, which the callers run first). Callers must have bumped generation
// BEFORE any teardown so stale goroutines bail — this method deliberately does
// not bump it again. Runs OUTSIDE s.mu: components self-lock, and
// snapshotter.Rebind does disk I/O that must never sit inside the session lock.
func (s *Session) resetHarnessState() {
	s.mu.Lock()
	s.backgroundWakeSuppressed = false
	s.mu.Unlock()

	s.resetContextWindowState()

	s.turn.reset()
	s.metrics.reset()
	s.prompt.resetInvocations()
	// Reset/SwitchSession install their target model explicitly; the captured
	// baseline belongs to the old conversation and must not leak a restore.
	s.model.clearOverride()
	// retryAttempt is per-run; a leftover value would mislabel the next run's
	// first retry event. activeRun is left alone: runs cannot span a switch
	// (idle contract) and ending another owner's span here would double-End.
	s.run.takeRetryAttempt()
	// Repoint at the new session's persisted undo stack (empty for a fresh
	// session); prior snapshots no longer map to this conversation.
	if s.deps.snapshotter != nil {
		s.deps.snapshotter.Rebind(s.undoStatePath())
	}
}
