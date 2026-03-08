package agent

import (
	"fmt"
	"os"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/storage"
)

func (s *Session) Prompt(text string) error {
	if s.beforePrompt != nil {
		s.beforePrompt()
	}
	s.context.microcompact()
	return s.agent.Prompt(text)
}

func (s *Session) PromptWithBlocks(blocks []agentcore.ContentBlock) error {
	if s.beforePrompt != nil {
		s.beforePrompt()
	}
	s.context.microcompact()
	msg := agentcore.Message{
		Role:      agentcore.RoleUser,
		Content:   blocks,
		Timestamp: time.Now(),
	}
	return s.agent.PromptMessages(msg)
}

func (s *Session) SetBeforePrompt(fn func()) {
	s.beforePrompt = fn
}

func (s *Session) Steer(text string) {
	s.agent.Steer(agentcore.UserMsg(text))
}

func (s *Session) Abort() {
	s.agent.Abort()
}

func (s *Session) AbortSilent() {
	s.agent.AbortSilent()
}

func (s *Session) IsRunning() bool {
	return s.agent.State().IsRunning
}

func (s *Session) ClearConversation() {
	s.agent.ClearMessages()
	s.agent.ClearAllQueues()
}

func (s *Session) SetModel(prov, model string) error {
	apiKey, baseURL := s.resolveCredentials(prov)
	s.mu.Lock()
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
	s.chatModel = chatModel
	s.maxTokensReduced = false
	s.mu.Unlock()

	s.emit(SessionEvent{
		Type:      SEModelChanged,
		ModelName: model,
		Provider:  prov,
	})

	s.reclampThinking()

	s.mu.Lock()
	thinkLvl := s.settings.ThinkingLevel
	s.mu.Unlock()
	if err := config.PatchGlobalSettings(config.Settings{
		Model:         &model,
		ThinkingLevel: &thinkLvl,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persist model setting: %v\n", err)
	}

	return nil
}

func (s *Session) ResolveAndSetModel(pattern string) (string, error) {
	if s.registry == nil {
		return "", fmt.Errorf("no model registry configured")
	}
	entry, thinkingLevel, err := s.registry.Resolve(pattern)
	if err != nil {
		return "", err
	}

	if err := s.SetModel(entry.Provider, entry.ID); err != nil {
		return "", err
	}

	if entry.ContextWindow > 0 {
		s.agent.SetContextWindow(entry.ContextWindow)
		s.mu.Lock()
		s.settings.ContextWindow = entry.ContextWindow
		s.mu.Unlock()
	}

	if thinkingLevel != "" {
		s.SetThinkingLevel(thinkingLevel)
	}
	return entry.ID, nil
}

func (s *Session) SetThinkingLevel(level agentcore.ThinkingLevel) {
	if s.registry != nil {
		s.mu.Lock()
		modelName := s.modelName
		s.mu.Unlock()
		available := s.registry.AvailableThinkingLevels(modelName)
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

	lvl := string(level)
	if err := config.PatchGlobalSettings(config.Settings{
		ThinkingLevel: &lvl,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persist thinking level setting: %v\n", err)
	}
}

func (s *Session) ModelName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelName
}

func (s *Session) Provider() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.provider
}

func (s *Session) APIKey() string {
	apiKey, _ := s.resolveCredentials(s.Provider())
	return apiKey
}

func (s *Session) BaseURL() string {
	_, baseURL := s.resolveCredentials(s.Provider())
	return baseURL
}

func (s *Session) resolveCredentials(prov string) (apiKey, baseURL string) {
	s.mu.Lock()
	pc, ok := s.providers[prov]
	s.mu.Unlock()
	if ok && pc.APIKey != "" {
		return pc.APIKey, pc.BaseURL
	}
	return config.EnvCredentials(prov)
}

func (s *Session) reclampThinking() {
	if s.registry == nil {
		return
	}
	s.mu.Lock()
	current := s.settings.ThinkingLevel
	modelName := s.modelName
	s.mu.Unlock()

	if current == "" {
		return
	}
	available := s.registry.AvailableThinkingLevels(modelName)
	clamped := provider.ClampThinkingLevel(current, available)
	if clamped != current {
		s.SetThinkingLevel(agentcore.ThinkingLevel(clamped))
	}
}

func (s *Session) NewSession() error {
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

func (s *Session) SwitchSession(id string) error {
	s.mu.Lock()
	oldStore := s.store
	mgr := s.mgr
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

	targetKey, targetBase := s.resolveCredentials(targetProvider)

	var restoredModel agentcore.ChatModel
	if snapshot.Model != "" || snapshot.Provider != "" {
		restoredModel, err = s.createModel(targetProvider, targetModel, targetKey, targetBase)
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

func (s *Session) Settings() config.Resolved {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

func (s *Session) ContextUsage() *agentcore.ContextUsage {
	return s.agent.ContextUsage()
}

func (s *Session) TotalTokens() int {
	return s.agent.TotalUsage().TotalTokens
}

func (s *Session) Registry() *provider.ModelRegistry {
	return s.registry
}

func (s *Session) CostEstimate() (inputTokens, outputTokens int, cost float64) {
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

func (s *Session) LastAssistantText() string {
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

func (s *Session) CurrentSessionInfo() (storage.SessionInfo, error) {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()

	if store == nil {
		return storage.SessionInfo{}, fmt.Errorf("no active session")
	}

	h := store.Header()
	return storage.SessionInfo{
		ID:      h.SessionID,
		Name:    h.Name,
		Path:    store.Path(),
		Cwd:     h.Cwd,
		Created: h.Created,
	}, nil
}

func (s *Session) ListSessions() ([]storage.SessionInfo, error) {
	s.mu.Lock()
	mgr := s.mgr
	s.mu.Unlock()

	if mgr == nil {
		return nil, fmt.Errorf("no session manager configured")
	}
	return mgr.List()
}

func (s *Session) Close() {
	if s.unsub != nil {
		s.unsub()
	}
	s.persistence.flushPendingMessages()
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store != nil {
		store.Close()
	}
}
