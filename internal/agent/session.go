package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/session"
)

// ModelFactory creates a chat model instance for a provider/model tuple.
type ModelFactory func(prov, model, apiKey, baseURL string) (agentcore.ChatModel, error)

// AgentSessionConfig configures a new AgentSession.
type AgentSessionConfig struct {
	Agent    *agentcore.Agent
	Store    *session.Store
	Manager  *session.Manager
	Registry *provider.ModelRegistry
	Settings config.Resolved
	Cwd      string
	// CreateModel allows tests/integrations to override model construction.
	// Defaults to provider.CreateModel when nil.
	CreateModel ModelFactory
	// LazyPersist buffers user messages and flushes them only when
	// an assistant response arrives. Disabled by default for safety.
	LazyPersist bool

	// Tools is the full set of tools registered with the agent.
	// Used by ToolsByName / RestoreAllTools for plan mode filtering.
	Tools []agentcore.Tool
	// SystemPrompt is the base system prompt passed to the agent.
	// Used by SetSystemSuffix to append/restore contextual instructions.
	SystemPrompt string
}

// AgentSession is the business-logic core that wraps Agent + session persistence.
// It is independent of any UI framework and drives interactive, print, and RPC modes.
type AgentSession struct {
	agent    *agentcore.Agent
	store    *session.Store
	mgr      *session.Manager
	registry *provider.ModelRegistry
	settings config.Resolved

	provider  string
	modelName string
	apiKey    string
	baseURL   string
	cwd       string

	createModel ModelFactory

	// Full tool set and base prompt for plan mode tool/prompt switching.
	allTools   []agentcore.Tool
	basePrompt string

	listeners []func(SessionEvent)
	unsub     func() // unsubscribe from agent events

	// Retry/overflow tracking (set by handleAgentEvent, reset on EventAgentEnd).
	retryAttempt     int
	overflowDetected bool

	// Lazy persistence: buffer user messages until assistant responds.
	lazyPersist    bool
	pendingUserMsg []agentcore.Message

	// Auto-naming: set once from first user message text.
	autoNamed bool

	mu sync.Mutex
}

// NewAgentSession creates an AgentSession and wires auto-persist to the agent.
func NewAgentSession(cfg AgentSessionConfig) *AgentSession {
	modelFactory := cfg.CreateModel
	if modelFactory == nil {
		modelFactory = provider.CreateModel
	}

	s := &AgentSession{
		agent:       cfg.Agent,
		store:       cfg.Store,
		mgr:         cfg.Manager,
		registry:    cfg.Registry,
		settings:    cfg.Settings,
		provider:    cfg.Settings.DefaultProvider,
		modelName:   cfg.Settings.DefaultModel,
		apiKey:      cfg.Settings.APIKey,
		baseURL:     cfg.Settings.BaseURL,
		cwd:         cfg.Cwd,
		createModel: modelFactory,
		lazyPersist: cfg.LazyPersist,
		allTools:    cfg.Tools,
		basePrompt:  cfg.SystemPrompt,
	}

	// Subscribe to agent events for auto-persistence and event forwarding.
	s.unsub = cfg.Agent.Subscribe(s.handleAgentEvent)
	return s
}

// --------------------------------------------------------------------------
// Core lifecycle
// --------------------------------------------------------------------------

// Prompt sends a user message to the agent.
func (s *AgentSession) Prompt(text string) error {
	return s.agent.Prompt(text)
}

// PromptMessages sends arbitrary messages to the agent.
func (s *AgentSession) PromptMessages(msgs ...agentcore.AgentMessage) error {
	return s.agent.PromptMessages(msgs...)
}

// Steer queues a steering message to interrupt the agent mid-run.
func (s *AgentSession) Steer(text string) {
	s.agent.Steer(agentcore.UserMsg(text))
}

// FollowUp queues a follow-up message for after the agent finishes.
func (s *AgentSession) FollowUp(text string) {
	s.agent.FollowUp(agentcore.UserMsg(text))
}

// Continue resumes the agent from its current context.
func (s *AgentSession) Continue() error {
	return s.agent.Continue()
}

// Abort cancels the running agent.
func (s *AgentSession) Abort() {
	s.agent.Abort()
}

// WaitForIdle blocks until the agent finishes its current run.
func (s *AgentSession) WaitForIdle() {
	s.agent.WaitForIdle()
}

// IsRunning reports whether the underlying agent is currently processing.
func (s *AgentSession) IsRunning() bool {
	return s.agent.State().IsRunning
}

// ClearConversation clears in-memory conversation messages and queued inputs.
func (s *AgentSession) ClearConversation() {
	s.agent.ClearMessages()
	s.agent.ClearAllQueues()
}

// --------------------------------------------------------------------------
// Model management
// --------------------------------------------------------------------------

// SetModel switches the LLM model and persists the change.
func (s *AgentSession) SetModel(prov, model, apiKey string) error {
	s.mu.Lock()
	baseURL := s.baseURL
	store := s.store
	s.mu.Unlock()
	chatModel, err := s.createModel(prov, model, apiKey, baseURL)
	if err != nil {
		return fmt.Errorf("create model %s/%s: %w", prov, model, err)
	}

	if store != nil {
		if err := store.AppendModelChange(prov, model); err != nil {
			return fmt.Errorf("persist model change: %w", err)
		}
	}

	s.agent.SetModel(chatModel)

	s.mu.Lock()
	s.provider = prov
	s.modelName = model
	s.apiKey = apiKey
	s.mu.Unlock()

	s.emit(SessionEvent{
		Type:      SEModelChanged,
		ModelName: model,
		Provider:  prov,
	})

	// Re-clamp thinking level for the new model.
	s.reclampThinking()

	return nil
}

// ResolveAndSetModel resolves a model pattern via the registry and switches to it.
// Returns the resolved model name.
func (s *AgentSession) ResolveAndSetModel(pattern string) (string, error) {
	if s.registry == nil {
		return "", fmt.Errorf("no model registry configured")
	}
	entry, thinkingLevel, err := s.registry.Resolve(pattern)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	apiKey := s.apiKey
	s.mu.Unlock()

	if err := s.SetModel(entry.Provider, entry.ID, apiKey); err != nil {
		return "", err
	}
	if thinkingLevel != "" {
		s.SetThinkingLevel(thinkingLevel)
	}
	return entry.ID, nil
}

// SetThinkingLevel changes the reasoning depth and persists the change.
func (s *AgentSession) SetThinkingLevel(level agentcore.ThinkingLevel) {
	// Clamp to nearest valid level for current model.
	if s.registry != nil {
		s.mu.Lock()
		model := s.modelName
		s.mu.Unlock()
		available := s.registry.AvailableThinkingLevels(model)
		level = agentcore.ThinkingLevel(provider.ClampThinkingLevel(string(level), available))
	}

	s.agent.SetThinkingLevel(level)

	s.mu.Lock()
	store := s.store
	s.settings.ThinkingLevel = string(level)
	s.mu.Unlock()

	if store != nil {
		if err := store.AppendThinkingLevelChange(string(level)); err != nil {
			s.emit(SessionEvent{
				Type:  SEError,
				Error: fmt.Errorf("persist thinking level: %w", err),
			})
		}
	}

	s.emit(SessionEvent{
		Type:  SEThinkingChanged,
		Level: level,
	})
}

// ModelName returns the current model name.
func (s *AgentSession) ModelName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelName
}

// Provider returns the current provider name.
func (s *AgentSession) Provider() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.provider
}

// APIKey returns the current API key.
func (s *AgentSession) APIKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apiKey
}

// BaseURL returns the current base URL.
func (s *AgentSession) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseURL
}

// --------------------------------------------------------------------------
// Session operations
// --------------------------------------------------------------------------

// NewSession closes the current session and creates a new one.
func (s *AgentSession) NewSession() error {
	s.mu.Lock()
	oldStore := s.store
	mgr := s.mgr
	cwd := s.cwd
	s.mu.Unlock()

	newStore, err := mgr.Create(cwd)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	s.agent.ClearMessages()
	s.agent.ClearAllQueues()

	s.mu.Lock()
	s.store = newStore
	s.autoNamed = false
	s.mu.Unlock()
	if oldStore != nil {
		_ = oldStore.Close()
	}

	s.emit(SessionEvent{
		Type:      SESessionSwitched,
		SessionID: newStore.Header().SessionID,
	})
	return nil
}

// SwitchSession closes the current session and restores another by ID.
func (s *AgentSession) SwitchSession(id string) error {
	s.mu.Lock()
	oldStore := s.store
	mgr := s.mgr
	apiKey := s.apiKey
	baseURL := s.baseURL
	curProvider := s.provider
	curModel := s.modelName
	s.mu.Unlock()

	newStore, err := mgr.Open(id)
	if err != nil {
		return fmt.Errorf("switch session %s: %w", id, err)
	}
	closeNewOnError := true
	defer func() {
		if closeNewOnError {
			_ = newStore.Close()
		}
	}()

	// Restore messages from the tree context.
	snapshot, err := newStore.BuildSnapshot()
	if err != nil {
		return fmt.Errorf("load session context: %w", err)
	}

	targetProvider := curProvider
	if snapshot.Provider != "" {
		targetProvider = snapshot.Provider
	}
	targetModel := curModel
	if snapshot.Model != "" {
		targetModel = snapshot.Model
	}

	var restoredModel agentcore.ChatModel
	if snapshot.Model != "" || snapshot.Provider != "" {
		restoredModel, err = s.createModel(targetProvider, targetModel, apiKey, baseURL)
		if err != nil {
			return fmt.Errorf("restore model %s/%s: %w", targetProvider, targetModel, err)
		}
	}

	s.agent.ClearMessages()
	s.agent.ClearAllQueues()
	if len(snapshot.Messages) > 0 {
		if err := s.agent.SetMessages(snapshot.Messages); err != nil {
			return fmt.Errorf("restore messages: %w", err)
		}
	}
	if snapshot.Thinking != "" {
		thinkingLevel := agentcore.ThinkingLevel(snapshot.Thinking)
		if s.registry != nil {
			available := s.registry.AvailableThinkingLevels(targetModel)
			thinkingLevel = agentcore.ThinkingLevel(provider.ClampThinkingLevel(string(thinkingLevel), available))
		}
		s.agent.SetThinkingLevel(thinkingLevel)
	}
	if restoredModel != nil {
		s.agent.SetModel(restoredModel)
	}

	s.mu.Lock()
	s.store = newStore
	s.provider = targetProvider
	s.modelName = targetModel
	s.autoNamed = newStore.Header().Name != ""
	if snapshot.Thinking != "" {
		clamped := snapshot.Thinking
		if s.registry != nil {
			available := s.registry.AvailableThinkingLevels(targetModel)
			clamped = provider.ClampThinkingLevel(snapshot.Thinking, available)
		}
		s.settings.ThinkingLevel = clamped
	}
	s.mu.Unlock()

	if oldStore != nil {
		_ = oldStore.Close()
	}
	closeNewOnError = false

	s.emit(SessionEvent{
		Type:      SESessionSwitched,
		SessionID: newStore.Header().SessionID,
	})
	return nil
}

// SetSessionName updates the display name of the current session.
func (s *AgentSession) SetSessionName(name string) error {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()

	if store == nil {
		return fmt.Errorf("no active session")
	}
	return store.SetName(name)
}

// Fork creates a branch from a specific entry ID.
// The agent's context is rebuilt from that point.
func (s *AgentSession) Fork(entryID string) error {
	s.mu.Lock()
	store := s.store
	apiKey := s.apiKey
	baseURL := s.baseURL
	curProvider := s.provider
	curModel := s.modelName
	s.mu.Unlock()

	if store == nil {
		return fmt.Errorf("no active session")
	}

	prevLeaf := store.LeafID()

	// Generate branch summary before fork (non-fatal on failure).
	currentMsgs := s.agent.Messages()
	if len(currentMsgs) > 2 {
		ctx := context.Background()
		if summary, err := s.generateBranchSummary(ctx, currentMsgs); err == nil && summary != "" {
			_ = store.AppendBranchSummary(prevLeaf, summary)
		}
	}

	exists, err := store.HasEntry(entryID)
	if err != nil {
		return fmt.Errorf("check fork entry: %w", err)
	}
	if !exists {
		return fmt.Errorf("entry %s not found", entryID)
	}

	store.SetLeafID(entryID)

	// Rebuild agent context from the new branch point.
	snapshot, err := store.BuildSnapshot()
	if err != nil {
		store.SetLeafID(prevLeaf)
		return fmt.Errorf("rebuild context after fork: %w", err)
	}

	targetProvider := curProvider
	if snapshot.Provider != "" {
		targetProvider = snapshot.Provider
	}
	targetModel := curModel
	if snapshot.Model != "" {
		targetModel = snapshot.Model
	}

	var restoredModel agentcore.ChatModel
	if snapshot.Model != "" || snapshot.Provider != "" {
		restoredModel, err = s.createModel(targetProvider, targetModel, apiKey, baseURL)
		if err != nil {
			store.SetLeafID(prevLeaf)
			return fmt.Errorf("restore model after fork %s/%s: %w", targetProvider, targetModel, err)
		}
	}

	s.agent.ClearMessages()
	s.agent.ClearAllQueues()
	if len(snapshot.Messages) > 0 {
		if err := s.agent.SetMessages(snapshot.Messages); err != nil {
			store.SetLeafID(prevLeaf)
			return fmt.Errorf("restore messages after fork: %w", err)
		}
	}

	s.mu.Lock()
	s.provider = targetProvider
	s.modelName = targetModel
	s.autoNamed = store.Header().Name != ""
	if snapshot.Thinking != "" {
		s.settings.ThinkingLevel = snapshot.Thinking
	}
	s.mu.Unlock()

	if snapshot.Thinking != "" {
		s.agent.SetThinkingLevel(agentcore.ThinkingLevel(snapshot.Thinking))
	}
	if restoredModel != nil {
		s.agent.SetModel(restoredModel)
	}
	return nil
}

// --------------------------------------------------------------------------
// Tool / prompt switching (plan mode support)
// --------------------------------------------------------------------------

// ToolsByName returns the subset of allTools matching the given names.
func (s *AgentSession) ToolsByName(names ...string) []agentcore.Tool {
	allowed := make(map[string]struct{}, len(names))
	for _, n := range names {
		allowed[n] = struct{}{}
	}
	var result []agentcore.Tool
	for _, t := range s.allTools {
		if _, ok := allowed[t.Name()]; ok {
			result = append(result, t)
		}
	}
	return result
}

// SetTools directly replaces the agent's active tool set.
func (s *AgentSession) SetTools(tools ...agentcore.Tool) {
	s.agent.SetTools(tools...)
}

// RestoreAllTools restores the full tool set, optionally appending extra tools.
func (s *AgentSession) RestoreAllTools(extra ...agentcore.Tool) {
	if len(extra) == 0 {
		s.agent.SetTools(s.allTools...)
		return
	}
	tools := make([]agentcore.Tool, len(s.allTools), len(s.allTools)+len(extra))
	copy(tools, s.allTools)
	tools = append(tools, extra...)
	s.agent.SetTools(tools...)
}

// SetSystemSuffix appends a suffix to the base system prompt.
// An empty suffix restores the original prompt.
func (s *AgentSession) SetSystemSuffix(suffix string) {
	if suffix == "" {
		s.agent.SetSystemPrompt(s.basePrompt)
		return
	}
	s.agent.SetSystemPrompt(s.basePrompt + "\n\n" + suffix)
}

// --------------------------------------------------------------------------
// Event subscription
// --------------------------------------------------------------------------

// Subscribe registers a listener for session events. Returns an unsubscribe function.
func (s *AgentSession) Subscribe(fn func(SessionEvent)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
	idx := len(s.listeners) - 1
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.listeners[idx] = nil
	}
}

// --------------------------------------------------------------------------
// Query operations
// --------------------------------------------------------------------------

// Settings returns current resolved settings.
func (s *AgentSession) Settings() config.Resolved {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// ContextUsage returns current context window occupancy.
func (s *AgentSession) ContextUsage() *agentcore.ContextUsage {
	return s.agent.ContextUsage()
}

// TotalTokens returns accumulated total tokens across current process lifetime.
func (s *AgentSession) TotalTokens() int {
	return s.agent.TotalUsage().TotalTokens
}

// Registry returns the model registry (may be nil).
func (s *AgentSession) Registry() *provider.ModelRegistry {
	return s.registry
}

// CostEstimate returns accumulated token counts and an estimated cost (USD) for the session.
// Cost is computed using the current model's rates (approximate if models were switched).
func (s *AgentSession) CostEstimate() (inputTokens, outputTokens int, cost float64) {
	usage := s.agent.TotalUsage()
	inputTokens = usage.Input
	outputTokens = usage.Output

	s.mu.Lock()
	model := s.modelName
	reg := s.registry
	s.mu.Unlock()

	if reg != nil {
		inRate, outRate, crRate, cwRate := reg.CostRates(model)
		cost = float64(inputTokens)*inRate/1e6 +
			float64(outputTokens)*outRate/1e6 +
			float64(usage.CacheRead)*crRate/1e6 +
			float64(usage.CacheWrite)*cwRate/1e6
	}
	return
}

// LastAssistantText returns the text of the most recent assistant message, or "".
func (s *AgentSession) LastAssistantText() string {
	msgs := s.agent.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, ok := msgs[i].(agentcore.Message)
		if !ok || msg.Role != agentcore.RoleAssistant {
			continue
		}
		if text := msg.TextContent(); text != "" {
			return text
		}
	}
	return ""
}

// CurrentSessionInfo returns metadata of the active session.
func (s *AgentSession) CurrentSessionInfo() (session.SessionInfo, error) {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()

	if store == nil {
		return session.SessionInfo{}, fmt.Errorf("no active session")
	}

	h := store.Header()
	return session.SessionInfo{
		ID:      h.SessionID,
		Name:    h.Name,
		Path:    store.Path(),
		Cwd:     h.Cwd,
		Created: h.Created,
	}, nil
}

// ListSessions returns sessions sorted by most recent update first.
func (s *AgentSession) ListSessions() ([]session.SessionInfo, error) {
	s.mu.Lock()
	mgr := s.mgr
	s.mu.Unlock()

	if mgr == nil {
		return nil, fmt.Errorf("no session manager configured")
	}
	return mgr.List()
}

// SessionTree builds tree data for the active session and returns current leaf ID.
func (s *AgentSession) SessionTree() (*session.TreeNode, string, error) {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()

	if store == nil {
		return nil, "", fmt.Errorf("no active session")
	}

	entries, err := store.ReadAllEntries()
	if err != nil {
		return nil, "", fmt.Errorf("read entries: %w", err)
	}
	root, err := session.BuildTree(entries)
	if err != nil {
		return nil, "", fmt.Errorf("build tree: %w", err)
	}
	return root, store.LeafID(), nil
}

// SetLabel sets or clears a label on a session entry.
// An empty label string clears the label.
func (s *AgentSession) SetLabel(targetID, label string) error {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()

	if store == nil {
		return fmt.Errorf("no active session")
	}
	return store.AppendLabel(targetID, label)
}

// Labels returns all labels in the current session.
func (s *AgentSession) Labels() (map[string]string, error) {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()

	if store == nil {
		return nil, fmt.Errorf("no active session")
	}
	return store.Labels()
}

// Close cleans up the session (unsubscribes from agent, closes store).
func (s *AgentSession) Close() {
	if s.unsub != nil {
		s.unsub()
	}
	s.flushPendingMessages()
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store != nil {
		store.Close()
	}
}

// --------------------------------------------------------------------------
// Internal
// --------------------------------------------------------------------------

// handleAgentEvent processes agent events for auto-persistence and forwarding.
func (s *AgentSession) handleAgentEvent(ev agentcore.Event) {
	// Auto-persist messages to session file.
	if ev.Type == agentcore.EventMessageEnd {
		if msg, ok := ev.Message.(agentcore.Message); ok {
			s.mu.Lock()
			lazy := s.lazyPersist
			s.mu.Unlock()

			if lazy && msg.Role == agentcore.RoleUser {
				// Buffer user messages until assistant responds.
				s.mu.Lock()
				s.pendingUserMsg = append(s.pendingUserMsg, msg)
				s.mu.Unlock()
			} else {
				// Flush any pending user messages before writing assistant response.
				if lazy && msg.Role == agentcore.RoleAssistant {
					s.flushPendingMessages()
				}
				s.persistMessage(msg)
			}

			// Auto-name session from first user message when the first assistant response arrives.
			if msg.Role == agentcore.RoleAssistant {
				s.tryAutoName()
			}
		}
	}

	// Bridge retry events from agentcore to session-level events.
	if ev.Type == agentcore.EventRetry && ev.RetryInfo != nil {
		if agentcore.IsContextOverflow(ev.RetryInfo.Err) {
			s.mu.Lock()
			s.overflowDetected = true
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			s.retryAttempt = ev.RetryInfo.Attempt
			s.mu.Unlock()
			s.emit(SessionEvent{
				Type:         SEAutoRetryStart,
				RetryAttempt: ev.RetryInfo.Attempt,
				RetryMax:     ev.RetryInfo.MaxRetries,
				RetryDelay:   ev.RetryInfo.Delay,
			})
		}
	}

	// On agent completion: flush pending messages, finalize retry state, handle overflow compaction, then threshold check.
	if ev.Type == agentcore.EventAgentEnd {
		s.flushPendingMessages()

		s.mu.Lock()
		retryAttempt := s.retryAttempt
		overflow := s.overflowDetected
		s.retryAttempt = 0
		s.overflowDetected = false
		s.mu.Unlock()

		if retryAttempt > 0 {
			s.emit(SessionEvent{
				Type:         SEAutoRetryEnd,
				RetryAttempt: retryAttempt,
				RetrySuccess: true,
			})
		}

		if overflow {
			if err := s.compactWithReason("overflow"); err == nil {
				// Auto-continue after overflow compaction (like pi-mono's willRetry=true).
				go func() {
					_ = s.agent.Continue()
				}()
				return // Don't forward EventAgentEnd yet — agent will restart.
			}
		} else {
			s.checkAutoCompaction()
		}
	}

	// Forward as session event.
	s.emit(SessionEvent{
		Type:       SEAgentEvent,
		AgentEvent: &ev,
	})
}

// persistMessage writes a single message to the session store.
func (s *AgentSession) persistMessage(msg agentcore.Message) {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store != nil {
		if err := store.AppendMessage(msg); err != nil {
			s.emit(SessionEvent{
				Type:  SEError,
				Error: fmt.Errorf("persist message: %w", err),
			})
		}
	}
}

// flushPendingMessages writes all buffered user messages to the store.
func (s *AgentSession) flushPendingMessages() {
	s.mu.Lock()
	pending := s.pendingUserMsg
	s.pendingUserMsg = nil
	s.mu.Unlock()

	for _, msg := range pending {
		s.persistMessage(msg)
	}
}

// reclampThinking re-clamps the current thinking level to the new model's capabilities.
func (s *AgentSession) reclampThinking() {
	if s.registry == nil {
		return
	}
	s.mu.Lock()
	current := s.settings.ThinkingLevel
	model := s.modelName
	s.mu.Unlock()

	if current == "" {
		return
	}
	available := s.registry.AvailableThinkingLevels(model)
	clamped := provider.ClampThinkingLevel(current, available)
	if clamped != current {
		s.SetThinkingLevel(agentcore.ThinkingLevel(clamped))
	}
}

// tryAutoName sets the session name from the first user message text once.
// It claims the autoNamed flag atomically under the lock to prevent duplicates,
// then performs the disk write asynchronously to avoid blocking event dispatch.
func (s *AgentSession) tryAutoName() {
	s.mu.Lock()
	if s.autoNamed || s.store == nil {
		s.mu.Unlock()
		return
	}
	store := s.store
	// Claim immediately to prevent concurrent calls from duplicating the write.
	s.autoNamed = true
	s.mu.Unlock()

	if h := store.Header(); h.Name != "" {
		return
	}

	// Find first user message text from agent context.
	var name string
	for _, m := range s.agent.Messages() {
		msg, ok := m.(agentcore.Message)
		if !ok || msg.Role != agentcore.RoleUser {
			continue
		}
		text := msg.TextContent()
		if text == "" {
			continue
		}
		// Truncate to 50 characters and collapse newlines.
		if len([]rune(text)) > 50 {
			text = string([]rune(text)[:50])
		}
		name = strings.ReplaceAll(text, "\n", " ")
		break
	}

	if name != "" {
		go func() { _ = store.SetName(name) }()
	}
}

func (s *AgentSession) emit(ev SessionEvent) {
	s.mu.Lock()
	listeners := make([]func(SessionEvent), len(s.listeners))
	copy(listeners, s.listeners)
	s.mu.Unlock()

	for _, fn := range listeners {
		if fn != nil {
			fn(ev)
		}
	}
}
