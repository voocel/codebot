package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/storage"
)

func (s *Session) Prompt(text string) error {
	s.beginTurn()
	if s.beforePrompt != nil {
		s.beforePrompt()
	}
	if s.runtime != nil {
		s.runtime.beforePrompt()
	} else {
		s.context.microcompact()
	}

	var msgs []agentcore.AgentMessage
	if !s.preambleInjected && s.deferredToolsPreamble != "" {
		msgs = append(msgs, injectedUserMsg(s.deferredToolsPreamble))
		s.preambleInjected = true
	}
	msgs = append(msgs, s.buildUserMessage(agentcore.TextBlock(text)))
	return s.agent.PromptMessages(msgs...)
}

func (s *Session) PromptWithBlocks(blocks []agentcore.ContentBlock) error {
	s.beginTurn()
	if s.beforePrompt != nil {
		s.beforePrompt()
	}
	if s.runtime != nil {
		s.runtime.beforePrompt()
	} else {
		s.context.microcompact()
	}

	var msgs []agentcore.AgentMessage
	if !s.preambleInjected && s.deferredToolsPreamble != "" {
		msgs = append(msgs, injectedUserMsg(s.deferredToolsPreamble))
		s.preambleInjected = true
	}
	msgs = append(msgs, s.buildUserMessage(blocks...))
	return s.agent.PromptMessages(msgs...)
}

// buildUserMessage creates a user message with reminders prepended as text blocks.
func (s *Session) buildUserMessage(userBlocks ...agentcore.ContentBlock) agentcore.Message {
	s.mu.Lock()
	runtimeReminders := append([]string(nil), s.runtimeReminders...)
	s.runtimeReminders = nil
	s.runtimeReminderKeys = make(map[string]struct{})
	staticReminders := append([]string(nil), s.staticReminders...)
	s.mu.Unlock()

	if len(runtimeReminders) == 0 && len(staticReminders) == 0 {
		return agentcore.Message{
			Role:      agentcore.RoleUser,
			Content:   userBlocks,
			Timestamp: time.Now(),
		}
	}

	blocks := make([]agentcore.ContentBlock, 0, len(runtimeReminders)+len(staticReminders)+len(userBlocks))
	for _, r := range runtimeReminders {
		blocks = append(blocks, agentcore.TextBlock(r))
	}
	for _, r := range staticReminders {
		blocks = append(blocks, agentcore.TextBlock(r))
	}
	blocks = append(blocks, userBlocks...)
	return agentcore.Message{
		Role:      agentcore.RoleUser,
		Content:   blocks,
		Timestamp: time.Now(),
	}
}

func (s *Session) queueRuntimeReminder(key string, kind RuntimeReminderKind, reminder string) {
	if reminder == "" {
		return
	}

	s.mu.Lock()
	if s.runtimeReminderKeys == nil {
		s.runtimeReminderKeys = make(map[string]struct{})
	}
	if _, exists := s.runtimeReminderKeys[key]; exists {
		s.mu.Unlock()
		return
	}
	s.runtimeReminderKeys[key] = struct{}{}
	s.runtimeReminders = append(s.runtimeReminders, reminder)
	s.mu.Unlock()
	s.recordReminderMetric(kind)
	s.recordReminderSnapshot(kind, "next_prompt")

	s.emit(SessionEvent{
		Type:         SERuntimeReminder,
		Reminder:     reminder,
		ReminderKind: kind,
	})
}

// deliverRuntimeReminder prefers in-run steering and otherwise defers to the
// next explicit user prompt. It does not auto-resume idle runs.
func (s *Session) deliverRuntimeReminder(key string, kind RuntimeReminderKind, reminder string) {
	if reminder == "" {
		return
	}
	if s.trySteerRuntimeReminder(key, kind, reminder) {
		return
	}
	s.queueRuntimeReminder(key, kind, reminder)
}

// continueWithRuntimeReminder prefers in-run steering, then idle auto-resume
// via agent.Inject, and finally falls back to next-prompt injection.
func (s *Session) continueWithRuntimeReminder(key string, kind RuntimeReminderKind, reminder string) {
	if reminder == "" {
		return
	}
	if s.trySteerRuntimeReminder(key, kind, reminder) {
		return
	}
	if s.tryAutoResumeRuntimeReminder(key, kind, reminder) {
		return
	}
	s.queueRuntimeReminder(key, kind, reminder)
}

func (s *Session) trySteerRuntimeReminder(key string, kind RuntimeReminderKind, reminder string) bool {
	if !s.agent.State().IsRunning {
		return false
	}

	s.mu.Lock()
	if s.steeredReminderKeys == nil {
		s.steeredReminderKeys = make(map[string]struct{})
	}
	if _, exists := s.steeredReminderKeys[key]; exists {
		s.mu.Unlock()
		return true
	}
	s.steeredReminderKeys[key] = struct{}{}
	s.pendingReminderContinue = true
	s.mu.Unlock()

	s.recordReminderMetric(kind)
	s.recordReminderSnapshot(kind, "steer")
	s.agent.Steer(injectedUserMsg(reminder))
	s.emit(SessionEvent{
		Type:         SERuntimeReminder,
		Reminder:     reminder,
		ReminderKind: kind,
	})
	return true
}

func (s *Session) tryAutoResumeRuntimeReminder(key string, kind RuntimeReminderKind, reminder string) bool {
	if s.agent.State().IsRunning {
		return false
	}

	msgs := s.agent.Messages()
	if len(msgs) == 0 || msgs[len(msgs)-1].GetRole() != agentcore.RoleAssistant {
		return false
	}

	s.mu.Lock()
	if s.autoResumeReminderKeys == nil {
		s.autoResumeReminderKeys = make(map[string]struct{})
	}
	if _, exists := s.autoResumeReminderKeys[key]; exists {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	result, err := s.agent.Inject(injectedUserMsg(reminder))
	if err != nil {
		return false
	}
	if result.Disposition != agentcore.InjectResumedIdleRun {
		return false
	}

	s.mu.Lock()
	s.autoResumeReminderKeys[key] = struct{}{}
	s.mu.Unlock()

	s.recordReminderMetric(kind)
	s.recordReminderSnapshot(kind, "auto_continue")
	s.emit(SessionEvent{
		Type:         SERuntimeReminder,
		Reminder:     reminder,
		ReminderKind: kind,
	})
	return true
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
	provType := s.providerType(prov)
	apiKey, baseURL := s.resolveCredentials(prov)
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	chatModel, err := s.createModel(provType, model, apiKey, baseURL)
	if err != nil {
		return fmt.Errorf("create model %s/%s: %w", prov, model, err)
	}

	if store != nil {
		if err := store.AppendModelChange(prov, model); err != nil {
			return fmt.Errorf("persist model change: %w", err)
		}
	}

	s.agent.SetModel(chatModel)

	// Sync small_model: provider config > fallback to current model.
	smallModel := model
	s.mu.Lock()
	if pc, ok := s.providers[prov]; ok && pc.SmallModel != "" {
		smallModel = pc.SmallModel
	}
	s.provider = prov
	s.modelName = model
	s.chatModel = chatModel
	s.settings.SmallModel = smallModel
	s.mu.Unlock()

	s.emit(SessionEvent{
		Type:      SEModelChanged,
		ModelName: model,
		Provider:  prov,
	})

	s.reclampThinking()

	s.mu.Lock()
	thinkLvl := s.settings.ThinkingLevel
	curSmall := s.settings.SmallModel
	cwd := s.cwd
	s.mu.Unlock()

	patch := config.Settings{
		Provider:      &prov,
		Model:         &model,
		SmallModel:    &curSmall,
		ThinkingLevel: &thinkLvl,
	}
	if err := config.PatchGlobalSettings(patch); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persist global setting: %v\n", err)
	}
	if config.ProjectConfigExists(cwd) {
		if err := config.PatchProjectSettings(cwd, patch); err != nil {
			fmt.Fprintf(os.Stderr, "warning: persist project setting: %v\n", err)
		}
	}

	return nil
}

// providerType returns the protocol type for a provider key.
func (s *Session) providerType(prov string) string {
	s.mu.Lock()
	pc, ok := s.providers[prov]
	s.mu.Unlock()
	if ok {
		return pc.ProviderType(prov)
	}
	if t, ok := config.KnownProviderTypes[prov]; ok {
		return t
	}
	return "openai"
}

func (s *Session) ResolveAndSetModel(pattern string) (string, error) {
	// Extract :thinking suffix (e.g. "model:high").
	thinkingLevel := agentcore.ThinkingLevel("")
	if idx := strings.LastIndex(pattern, ":"); idx > 0 {
		suffix := pattern[idx+1:]
		if provider.IsValidThinkingLevel(suffix) {
			thinkingLevel = agentcore.ThinkingLevel(suffix)
			pattern = pattern[:idx]
		}
	}

	// Search across all configured providers' models lists.
	s.mu.Lock()
	provSnapshot := make(map[string]config.ProviderConfig, len(s.providers))
	for k, v := range s.providers {
		provSnapshot[k] = v
	}
	s.mu.Unlock()

	type match struct {
		provider string
		model    string
	}
	var matches []match
	for provName, pc := range provSnapshot {
		for _, m := range pc.Models {
			if strings.EqualFold(m, pattern) {
				matches = append(matches, match{provider: provName, model: m})
			}
		}
	}
	if len(matches) == 1 {
		m := matches[0]
		if err := s.SetModel(m.provider, m.model); err != nil {
			return "", err
		}
		s.updateContextFromRegistry(m.provider, m.model)
		if thinkingLevel != "" {
			s.SetThinkingLevel(thinkingLevel)
		}
		return m.model, nil
	}
	if len(matches) > 1 {
		provs := make([]string, len(matches))
		for i, m := range matches {
			provs[i] = config.FormatModelID(m.provider, m.model)
		}
		return "", fmt.Errorf("model %q is ambiguous, found in: %s; use provider/model format", pattern, strings.Join(provs, ", "))
	}

	// Fallback: try current provider with the pattern as model name.
	curProv := s.Provider()
	if err := s.SetModel(curProv, pattern); err != nil {
		return "", fmt.Errorf("model %q not found in any configured provider", pattern)
	}
	s.updateContextFromRegistry(curProv, pattern)
	if thinkingLevel != "" {
		s.SetThinkingLevel(thinkingLevel)
	}
	return pattern, nil
}

// updateContextFromRegistry updates context window from registry metadata if available.
// It tries provider-qualified lookup first (e.g. "anthropic/claude-sonnet-4-5"),
// then falls back to bare modelID for custom providers not in the registry.
func (s *Session) updateContextFromRegistry(providerKey, modelID string) {
	if s.registry == nil {
		return
	}
	// Try provider-qualified lookup using the protocol type.
	provType := s.providerType(providerKey)
	entry, _, err := s.registry.Resolve(provType + "/" + modelID)
	if err != nil {
		// Fallback to bare model ID for custom providers.
		entry, _, err = s.registry.Resolve(modelID)
	}
	if err != nil || entry.ContextWindow <= 0 {
		return
	}
	s.agent.SetContextWindow(entry.ContextWindow)
	s.mu.Lock()
	s.settings.ContextWindow = entry.ContextWindow
	s.mu.Unlock()
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
	s.resetHarnessStateLocked()
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
		targetType := s.providerType(targetProvider)
		restoredModel, err = s.createModel(targetType, targetModel, targetKey, targetBase)
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
		agentcore.ReactivateDeferred(s.allTools, snapshot.Messages)
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
	s.resetHarnessStateLocked()
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

func (s *Session) RecentToolCalls(limit int) []ToolCallSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 || limit > len(s.recentToolCalls) {
		limit = len(s.recentToolCalls)
	}
	start := len(s.recentToolCalls) - limit
	snapshots := make([]ToolCallSnapshot, 0, limit)
	for _, call := range s.recentToolCalls[start:] {
		snapshots = append(snapshots, ToolCallSnapshot{
			Tool:      call.Tool,
			ArgsHash:  call.ArgsHash,
			Success:   call.Success,
			Timestamp: call.Timestamp,
		})
	}
	return snapshots
}

func (s *Session) LastReminder() (ReminderSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastReminder == nil {
		return ReminderSnapshot{}, false
	}
	return *s.lastReminder, true
}

func (s *Session) LastCompaction() (CompactionSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastCompaction == nil {
		return CompactionSnapshot{}, false
	}
	return *s.lastCompaction, true
}

func (s *Session) LastRunSummary() (agentcore.RunSummary, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastRunSummary == nil {
		return agentcore.RunSummary{}, false
	}
	return *s.lastRunSummary, true
}

func (s *Session) TotalTokens() int {
	return s.agent.TotalUsage().TotalTokens
}

func (s *Session) Registry() *provider.ModelRegistry {
	return s.registry
}

func (s *Session) CostEstimate() (inputTokens, outputTokens int, cost float64) {
	usage := s.agent.TotalUsage()
	inputTokens = usage.Input + usage.CacheRead + usage.CacheWrite
	outputTokens = usage.Output

	s.mu.Lock()
	model := s.modelName
	reg := s.registry
	s.mu.Unlock()

	if reg != nil {
		inRate, outRate, crRate, cwRate := reg.CostRates(model)
		cost = float64(usage.Input)*inRate/1e6 +
			float64(outputTokens)*outRate/1e6 +
			float64(usage.CacheRead)*crRate/1e6 +
			float64(usage.CacheWrite)*cwRate/1e6
	}
	return
}

// Messages returns the current agent message history.
func (s *Session) Messages() []agentcore.AgentMessage {
	return s.agent.Messages()
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

func (s *Session) SessionID() string {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store == nil {
		return ""
	}
	return store.Header().SessionID
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

// SideQuestion sends a one-shot question to the current model using the full
// conversation context but without tool definitions or conversation history
// mutation. The answer is ephemeral — it never enters the session store.
// This powers the /btw side-chain Q&A feature.
func (s *Session) SideQuestion(ctx context.Context, question string) (string, error) {
	resp, err := s.ephemeralQuery(ctx, question)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Message.TextContent()), nil
}

// injectedUserMsg creates a user message marked as system-injected via metadata.
// Auto-naming and session listing skip messages with this marker.
func injectedUserMsg(text string) agentcore.Message {
	msg := agentcore.UserMsg(text)
	msg.Metadata = map[string]any{"injected": true}
	return msg
}

// ephemeralQuery sends a one-shot query to the current model using the full
// conversation context. Tool definitions are omitted and tool-related messages
// are stripped so the request reuses the parent conversation's prompt cache
// without breaking cache alignment. The result is never persisted.
// Both SideQuestion (/btw) and GenerateSuggestion share this path.
func (s *Session) ephemeralQuery(ctx context.Context, userText string, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	s.mu.Lock()
	model := s.chatModel
	s.mu.Unlock()

	if model == nil {
		return nil, fmt.Errorf("no model configured")
	}

	raw := s.agent.BuildLLMMessages()
	if len(raw) == 0 {
		return nil, fmt.Errorf("no conversation context")
	}

	// Strip tool-related content: providers reject tool references when no
	// tool definitions are provided. Keep system, user, and text-only assistant.
	msgs := make([]agentcore.Message, 0, len(raw))
	for _, m := range raw {
		switch m.Role {
		case agentcore.RoleSystem, agentcore.RoleUser:
			msgs = append(msgs, m)
		case agentcore.RoleAssistant:
			var textBlocks []agentcore.ContentBlock
			for _, b := range m.Content {
				if b.Type == agentcore.ContentText || b.Type == agentcore.ContentThinking {
					textBlocks = append(textBlocks, b)
				}
			}
			if len(textBlocks) > 0 {
				msgs = append(msgs, agentcore.Message{
					Role:    agentcore.RoleAssistant,
					Content: textBlocks,
				})
			}
		}
	}

	msgs = append(msgs, agentcore.Message{
		Role:    agentcore.RoleUser,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(userText)},
	})

	defaults := []agentcore.CallOption{agentcore.WithThinking(agentcore.ThinkingOff)}
	return model.Generate(ctx, msgs, nil, append(defaults, opts...)...)
}

// GenerateSuggestion calls the current model with a condensed conversation
// history plus a suggestion prompt to predict the user's next input.
// Returns "" when no suggestion is available (first turn, no model, etc.).
func (s *Session) GenerateSuggestion(ctx context.Context) (string, error) {
	resp, err := s.ephemeralQuery(ctx, config.SuggestionPrompt,
		agentcore.WithMaxTokens(500),
	)
	if err != nil {
		return "", err
	}

	text := strings.TrimSpace(resp.Message.TextContent())
	text = strings.Trim(text, "\"'`")
	text = strings.TrimSpace(text)

	if text == "" || strings.EqualFold(text, "NONE") || len(strings.Fields(text)) > 12 {
		return "", nil
	}
	return text, nil
}
