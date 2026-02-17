package agent

import (
	"fmt"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/session"
)

// Session defines the contract that UI layers and command handlers use
// to interact with the agent session.
type Session interface {
	// Lifecycle
	Prompt(text string) error
	PromptMessages(msgs ...agentcore.AgentMessage) error
	Abort()
	Continue() error
	Steer(text string)
	FollowUp(text string)
	WaitForIdle()

	// Model
	ModelName() string
	Provider() string
	APIKey() string
	BaseURL() string
	SetModel(prov, model, apiKey string) error
	ResolveAndSetModel(pattern string) (string, error)
	SetThinkingLevel(level agentcore.ThinkingLevel)

	// Session management
	NewSession() error
	SwitchSession(id string) error
	SetSessionName(name string) error
	Fork(entryID string) error
	Compact() error

	// Access
	Agent() *agentcore.Agent
	Store() *session.Store
	Manager() *session.Manager
	Registry() *provider.ModelRegistry
	Settings() config.Resolved
	Cwd() string
	ContextUsage() *agentcore.ContextUsage

	// Events
	Subscribe(fn func(SessionEvent)) func()

	// Cleanup
	Close()
}

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

	listeners []func(SessionEvent)
	unsub     func() // unsubscribe from agent events
	mu        sync.Mutex
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
	s.agent.SetThinkingLevel(level)

	s.mu.Lock()
	store := s.store
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
		s.agent.SetThinkingLevel(agentcore.ThinkingLevel(snapshot.Thinking))
	}
	if restoredModel != nil {
		s.agent.SetModel(restoredModel)
	}

	s.mu.Lock()
	s.store = newStore
	s.provider = targetProvider
	s.modelName = targetModel
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
// Accessors
// --------------------------------------------------------------------------

// Agent returns the underlying agentcore.Agent.
func (s *AgentSession) Agent() *agentcore.Agent { return s.agent }

// Store returns the current session store.
func (s *AgentSession) Store() *session.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store
}

// Manager returns the session manager.
func (s *AgentSession) Manager() *session.Manager { return s.mgr }

// Registry returns the model registry.
func (s *AgentSession) Registry() *provider.ModelRegistry { return s.registry }

// Settings returns the resolved settings.
func (s *AgentSession) Settings() config.Resolved { return s.settings }

// Cwd returns the working directory.
func (s *AgentSession) Cwd() string { return s.cwd }

// ContextUsage returns the current context window occupancy.
func (s *AgentSession) ContextUsage() *agentcore.ContextUsage {
	return s.agent.ContextUsage()
}

// Close cleans up the session (unsubscribes from agent, closes store).
func (s *AgentSession) Close() {
	if s.unsub != nil {
		s.unsub()
	}
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
	}

	// Check auto-compaction when agent finishes.
	if ev.Type == agentcore.EventAgentEnd {
		s.checkAutoCompaction()
	}

	// Forward as session event.
	s.emit(SessionEvent{
		Type:       SEAgentEvent,
		AgentEvent: &ev,
	})
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
