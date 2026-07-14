package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	agentctx "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/goal"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/telemetry"
)

var errStaleSessionGeneration = errors.New("stale session generation")

// runCtx is the base context for every agent-loop entry (Prompt / Continue /
// idle resume). It carries the session cwd as a LIVE source (WithCwdFunc), so a
// worktree switch mid-turn (RetargetWorkspace) is seen by the next tool call —
// same-turn edits land in the new workspace, without rebuilding any tools.
// Teammates capture a fixed cwd at spawn (see teammateCwd), so the live source
// never bleeds across them.
func (s *Session) baseRunCtx() context.Context {
	return tools.WithCwdFunc(context.Background(), func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.cwd
	})
}

func (s *Session) startPromptMessages(msgs ...agentcore.AgentMessage) error {
	ctx, run, previous := s.beginTelemetryRun(s.baseRunCtx())
	if err := s.agent.PromptMessages(ctx, msgs...); err != nil {
		s.rollbackTelemetryRun(run, previous, err)
		return err
	}
	s.commitTelemetryRun(previous)
	return nil
}

func (s *Session) startContinue() error {
	ctx, run, previous := s.beginTelemetryRun(s.baseRunCtx())
	if err := s.agent.Continue(ctx); err != nil {
		s.rollbackTelemetryRun(run, previous, err)
		return err
	}
	s.commitTelemetryRun(previous)
	return nil
}

func (s *Session) beginTelemetryRun(ctx context.Context) (context.Context, *telemetry.Run, *telemetry.Run) {
	if s.telemetryTracer == nil {
		return ctx, nil, nil
	}
	ctx, run := s.telemetryTracer.StartRun(ctx, "agent run")
	s.mu.Lock()
	previous := s.activeRun
	s.activeRun = run
	s.mu.Unlock()
	return ctx, run, previous
}

func (s *Session) commitTelemetryRun(previous *telemetry.Run) {
	if previous != nil {
		previous.End(nil)
	}
}

func (s *Session) rollbackTelemetryRun(run, previous *telemetry.Run, err error) {
	if run == nil {
		return
	}
	s.mu.Lock()
	if s.activeRun == run {
		s.activeRun = previous
	}
	s.mu.Unlock()
	run.End(err)
}

func (s *Session) endTelemetryRun(err error) {
	s.mu.Lock()
	run := s.activeRun
	s.activeRun = nil
	s.mu.Unlock()
	if run != nil {
		run.End(err)
	}
}

func (s *Session) Prompt(text string) error {
	var hookContext string
	if s.hookRunner != nil {
		dec, err := s.hookRunner.RunUserPromptSubmit(context.Background(), text)
		if err != nil {
			return err
		}
		hookContext = strings.TrimSpace(dec.AdditionalContext)
	}
	s.beginTurn()
	if s.beforePrompt != nil {
		s.beforePrompt()
	}
	if hookContext != "" {
		s.queueRuntimeReminder("hook_context", ReminderHookContext, wrapHookContext(hookContext))
	}
	s.runtime.beforeUserPrompt([]agentcore.ContentBlock{agentcore.TextBlock(text)})

	var msgs []agentcore.AgentMessage
	if !s.preambleInjected && s.deferredToolsPreamble != "" {
		msgs = append(msgs, injectedUserMsg(s.deferredToolsPreamble))
		s.preambleInjected = true
	}
	msgs = append(msgs, s.buildUserMessage(agentcore.TextBlock(text)))
	return s.startPromptMessages(msgs...)
}

func (s *Session) PromptWithBlocks(blocks []agentcore.ContentBlock) error {
	s.beginTurn()
	if s.beforePrompt != nil {
		s.beforePrompt()
	}
	s.runtime.beforeUserPrompt(blocks)

	var msgs []agentcore.AgentMessage
	if !s.preambleInjected && s.deferredToolsPreamble != "" {
		msgs = append(msgs, injectedUserMsg(s.deferredToolsPreamble))
		s.preambleInjected = true
	}
	msgs = append(msgs, s.buildUserMessage(blocks...))
	return s.startPromptMessages(msgs...)
}

// buildUserMessage creates a user message with reminders prepended as text blocks.
func (s *Session) buildUserMessage(userBlocks ...agentcore.ContentBlock) agentcore.Message {
	s.mu.Lock()
	runtimeReminders := append([]string(nil), s.reminders.runtime...)
	s.reminders.runtime = nil
	s.reminders.runtimeKeys = make(map[string]struct{})
	staticReminders := append([]string(nil), s.reminders.static...)
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
	if s.reminders.runtimeKeys == nil {
		s.reminders.runtimeKeys = make(map[string]struct{})
	}
	if _, exists := s.reminders.runtimeKeys[key]; exists {
		s.mu.Unlock()
		return
	}
	s.reminders.runtimeKeys[key] = struct{}{}
	s.reminders.runtime = append(s.reminders.runtime, reminder)
	s.mu.Unlock()
	s.recordReminderMetric(kind)
	s.recordReminderSnapshot(kind, "next_prompt")

	s.emit(SessionEvent{
		Type:         SERuntimeReminder,
		Reminder:     reminder,
		ReminderKind: kind,
	})
}

func (s *Session) continueIfCurrentGeneration(gen uint64) error {
	s.mu.Lock()
	if s.generation != gen {
		s.mu.Unlock()
		return errStaleSessionGeneration
	}
	s.mu.Unlock()
	return s.startContinue()
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
// via agent.InjectContext, and finally falls back to next-prompt injection.
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
	if s.reminders.steeredKeys == nil {
		s.reminders.steeredKeys = make(map[string]struct{})
	}
	if _, exists := s.reminders.steeredKeys[key]; exists {
		s.mu.Unlock()
		return true
	}
	s.reminders.steeredKeys[key] = struct{}{}
	s.reminders.pendingContinue = true
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
	if s.reminders.autoResumeKeys == nil {
		s.reminders.autoResumeKeys = make(map[string]struct{})
	}
	if _, exists := s.reminders.autoResumeKeys[key]; exists {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	ctx, run, previous := s.beginTelemetryRun(s.baseRunCtx())
	result, err := s.agent.InjectContext(ctx, injectedUserMsg(reminder))
	if err != nil {
		s.rollbackTelemetryRun(run, previous, err)
		return false
	}
	if result.Disposition != agentcore.InjectResumedIdleRun {
		s.rollbackTelemetryRun(run, previous, nil)
		return false
	}
	s.commitTelemetryRun(previous)

	s.mu.Lock()
	s.reminders.autoResumeKeys[key] = struct{}{}
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

// PlanModeSignal describes the plan-mode state observed at the moment the
// runtime polls before queueing a per-prompt reminder. Three distinct
// situations need different behavior:
//
//   - Active=true: plan mode is on; emit the sparse "still read-only" reminder
//     on its 5-turn cadence (PlanFilePath gives the writable plan file).
//   - JustCancelled=true: plan mode was cancelled via /plan cancel since the
//     last poll; emit a one-shot "you have exited plan mode" reminder so the
//     model knows the MUST-NOT rules buried in the EnterPlanMode tool_result
//     no longer apply.
//   - All zero: plan mode is off; no reminder needed.
//
// Active and JustCancelled are mutually exclusive — the producer (plan.Manager)
// guarantees that. JustCancelled is consumed on read: a second poll without a
// fresh Cancel() returns the zero value.
type PlanModeSignal struct {
	Active        bool
	PlanFilePath  string
	JustCancelled bool
}

// SetPlanModeSignal registers a callback the runtime polls before every user
// prompt to decide whether to queue a plan-mode reminder. plan.Manager wires
// this so the reminder cadence stays driven by the actual plan-mode state
// rather than a side-channel boolean.
func (s *Session) SetPlanModeSignal(fn func() PlanModeSignal) {
	s.mu.Lock()
	s.planModeSignal = fn
	s.mu.Unlock()
}

func (s *Session) currentPlanModeSignal() PlanModeSignal {
	s.mu.Lock()
	fn := s.planModeSignal
	s.mu.Unlock()
	if fn == nil {
		return PlanModeSignal{}
	}
	return fn()
}

// SetGoalSignal registers a callback the runtime polls at natural stop points
// to decide whether an explicit /goal should auto-continue.
func (s *Session) SetGoalSignal(fn func() goal.Signal) {
	s.mu.Lock()
	s.goalSignal = fn
	s.mu.Unlock()
}

func (s *Session) currentGoalSignal() goal.Signal {
	s.mu.Lock()
	fn := s.goalSignal
	s.mu.Unlock()
	if fn == nil {
		return goal.Signal{}
	}
	return fn()
}

func (s *Session) SetGoalUsageLimitHandler(fn func(string) (goal.State, error)) {
	s.mu.Lock()
	s.goalUsageLimit = fn
	s.mu.Unlock()
}

func (s *Session) markGoalUsageLimited(reason string) error {
	s.mu.Lock()
	fn := s.goalUsageLimit
	s.mu.Unlock()
	if fn == nil {
		return nil
	}
	_, err := fn(reason)
	return err
}

func (s *Session) HandleGoalChange(change goal.Change) {
	current := change.Current.Normalize()
	ev := SessionEvent{
		Type:         SEGoalUpdated,
		Goal:         current,
		GoalPrevious: change.Previous.Normalize(),
	}
	if current.Status == goal.StatusOff {
		ev.Type = SEGoalCleared
	}
	s.emit(ev)
}

func (s *Session) SetBeforePrompt(fn func()) {
	s.beforePrompt = fn
}

// SetIdleHook registers a callback invoked after each turn fully settles
// (EventAgentEnd with no pending compaction resume or queued reminder).
// Must be cheap on its fast path — it runs on the event dispatch goroutine.
func (s *Session) SetIdleHook(fn func()) {
	s.idleHook = fn
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
	// Conversation gone → LLM lost its read history. Drop file-read stamps
	// so the next write/edit forces a fresh read.
	if s.fileReadState != nil {
		s.fileReadState.Reset()
	}
}

func (s *Session) applyTemporarySkillModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}

	prov, resolved, chatModel, err := s.resolveModelOverride(model)
	if err != nil {
		return fmt.Errorf("resolve skill model %q: %w", model, err)
	}
	s.mu.Lock()
	currentThinking := s.settings.ReasoningEffort
	s.mu.Unlock()
	effectiveThinking, ok := resolveThinkingLevelForModelStrict(chatModel, currentThinking)
	if !ok {
		return fmt.Errorf("resolve skill model %q: unsupported reasoning_effort %q", model, currentThinking)
	}

	s.mu.Lock()
	if !s.skillRuntime.active {
		s.skillRuntime.active = true
		s.skillRuntime.baseProvider = s.provider
		s.skillRuntime.baseModel = s.modelName
		s.skillRuntime.baseChatModel = s.chatModel
		s.skillRuntime.baseThinking = s.settings.ReasoningEffort
	}
	s.provider = prov
	s.modelName = resolved
	s.chatModel = chatModel
	s.settings.ReasoningEffort = effectiveThinking
	s.mu.Unlock()

	s.agent.SetModel(chatModel)
	s.agent.SetThinkingLevel(agentcore.ThinkingLevel(effectiveThinking))
	return nil
}

func (s *Session) applyTemporarySkillThinking(level string) error {
	if level == "" {
		return nil
	}

	s.mu.Lock()
	if !s.skillRuntime.active {
		s.skillRuntime.active = true
		s.skillRuntime.baseProvider = s.provider
		s.skillRuntime.baseModel = s.modelName
		s.skillRuntime.baseChatModel = s.chatModel
		s.skillRuntime.baseThinking = s.settings.ReasoningEffort
	}
	s.mu.Unlock()

	requested := level
	var ok bool
	level, ok = s.resolveThinkingLevel(level)
	if !ok {
		return fmt.Errorf("unsupported reasoning_effort %q", requested)
	}
	s.agent.SetThinkingLevel(agentcore.ThinkingLevel(level))
	s.mu.Lock()
	s.settings.ReasoningEffort = level
	s.mu.Unlock()
	return nil
}

func (s *Session) applySkillPathHints(name string, paths []string) {
	if len(paths) == 0 {
		return
	}
	var lines []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p != "" {
			lines = append(lines, "- "+p)
		}
	}
	if len(lines) == 0 {
		return
	}
	reminder := "<system-reminder>\n"
	if strings.TrimSpace(name) != "" {
		reminder += fmt.Sprintf("The active skill %q suggests focusing on these paths or patterns first:\n", name)
	} else {
		reminder += "The active skill suggests focusing on these paths or patterns first:\n"
	}
	reminder += strings.Join(lines, "\n")
	reminder += "\nIf you need to look beyond them, explain why before continuing.\n</system-reminder>"
	s.deliverRuntimeReminder("skill_paths:"+strings.Join(lines, "|"), ReminderSkillPaths, reminder)
}

func (s *Session) clearTemporarySkillOverrides() {
	s.mu.Lock()
	active := s.skillRuntime.active
	baseProvider := s.skillRuntime.baseProvider
	baseModel := s.skillRuntime.baseModel
	baseChatModel := s.skillRuntime.baseChatModel
	baseThinking := s.skillRuntime.baseThinking
	s.skillRuntime.active = false
	s.skillRuntime.baseProvider = ""
	s.skillRuntime.baseModel = ""
	s.skillRuntime.baseChatModel = nil
	s.skillRuntime.baseThinking = ""
	s.mu.Unlock()

	if !active {
		return
	}
	if baseChatModel != nil {
		s.agent.SetModel(baseChatModel)
	}
	effectiveThinking, ok := resolveThinkingLevelForModelStrict(baseChatModel, baseThinking)
	if !ok {
		s.emit(SessionEvent{
			Type:  SEError,
			Error: fmt.Errorf("unsupported reasoning_effort %q", baseThinking),
		})
		return
	}
	if effectiveThinking != "" {
		s.agent.SetThinkingLevel(agentcore.ThinkingLevel(effectiveThinking))
	} else {
		s.agent.SetThinkingLevel("")
	}
	s.mu.Lock()
	s.provider = baseProvider
	s.modelName = baseModel
	s.chatModel = baseChatModel
	s.settings.ReasoningEffort = baseThinking
	s.mu.Unlock()
}

func (s *Session) resolveModelOverride(pattern string) (string, string, agentcore.ChatModel, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", "", nil, fmt.Errorf("empty model override")
	}

	s.mu.Lock()
	curProv := s.provider
	provSnapshot := make(map[string]config.ProviderConfig, len(s.providers))
	maps.Copy(provSnapshot, s.providers)
	s.mu.Unlock()

	if strings.Contains(pattern, "/") {
		if prov, model, ok := strings.Cut(pattern, "/"); ok {
			provType, err := s.providerType(prov)
			if err != nil {
				return "", "", nil, err
			}
			apiKey, baseURL := s.resolveCredentials(prov)
			providerExtra := s.resolveProviderExtra(prov)
			chatModel, err := s.createModel(provType, model, apiKey, baseURL, providerExtra)
			if err == nil {
				return prov, model, chatModel, nil
			}
		}
	}

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
	switch len(matches) {
	case 1:
		m := matches[0]
		provType, err := s.providerType(m.provider)
		if err != nil {
			return "", "", nil, err
		}
		apiKey, baseURL := s.resolveCredentials(m.provider)
		providerExtra := s.resolveProviderExtra(m.provider)
		chatModel, err := s.createModel(provType, m.model, apiKey, baseURL, providerExtra)
		return m.provider, m.model, chatModel, err
	case 0:
		provType, err := s.providerType(curProv)
		if err != nil {
			return "", "", nil, err
		}
		apiKey, baseURL := s.resolveCredentials(curProv)
		providerExtra := s.resolveProviderExtra(curProv)
		chatModel, err := s.createModel(provType, pattern, apiKey, baseURL, providerExtra)
		if err != nil {
			return "", "", nil, err
		}
		return curProv, pattern, chatModel, nil
	default:
		var ids []string
		for _, m := range matches {
			ids = append(ids, config.FormatModelID(m.provider, m.model))
		}
		return "", "", nil, fmt.Errorf("ambiguous model override, matches: %s", strings.Join(ids, ", "))
	}
}

func (s *Session) SetModel(prov, model string) error {
	provType, err := s.providerType(prov)
	if err != nil {
		return err
	}
	apiKey, baseURL := s.resolveCredentials(prov)
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	providerExtra := s.resolveProviderExtra(prov)
	chatModel, err := s.createModel(provType, model, apiKey, baseURL, providerExtra)
	if err != nil {
		return fmt.Errorf("create model %s/%s: %w", prov, model, err)
	}
	currentThinking := s.Settings().ReasoningEffort
	resolvedThinking, ok := provider.ResolveThinkingLevel(chatModel, currentThinking)
	if !ok {
		return fmt.Errorf("switch model %s/%s: unsupported reasoning_effort %q", prov, model, currentThinking)
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
	s.settings.ReasoningEffort = resolvedThinking
	s.settings.SmallModel = smallModel
	s.mu.Unlock()

	s.emit(SessionEvent{
		Type:      SEModelChanged,
		ModelName: model,
		Provider:  prov,
	})

	s.agent.SetThinkingLevel(agentcore.ThinkingLevel(resolvedThinking))
	s.updateContextFromRegistry(prov, model)

	return nil
}

// providerType returns the protocol type for a provider key.
func (s *Session) providerType(prov string) (string, error) {
	s.mu.Lock()
	pc, ok := s.providers[prov]
	s.mu.Unlock()
	if ok {
		return pc.ProviderType(prov)
	}
	return config.ResolveProviderType(prov, "")
}

func (s *Session) resolveProviderExtra(prov string) map[string]any {
	s.mu.Lock()
	pc, ok := s.providers[prov]
	s.mu.Unlock()
	if ok {
		return pc.ProviderExtra()
	}
	return nil
}

// updateContextFromRegistry updates context window from registry metadata if available.
// It tries provider-qualified lookup first (e.g. "anthropic/claude-sonnet-4-5"),
// then falls back to bare modelID for custom providers not in the registry.
func (s *Session) updateContextFromRegistry(providerKey, modelID string) {
	if s.registry == nil {
		return
	}
	// Try provider-qualified lookup using the protocol type.
	provType, err := s.providerType(providerKey)
	if err == nil {
		entry, _, err := s.registry.Resolve(provType + "/" + modelID)
		if err == nil && entry.ContextWindow > 0 {
			s.applyContextWindow(entry.ContextWindow)
			return
		}
	}
	entry, _, err := s.registry.Resolve(modelID)
	if err != nil || entry.ContextWindow <= 0 {
		return
	}
	s.applyContextWindow(entry.ContextWindow)
}

func (s *Session) applyContextWindow(window int) {
	// Re-apply user-configured compaction caps to the new model window so that
	// mid-session model switches honor compact_window / compact_ratio.
	if cap := s.settings.CompactWindow; cap > 0 && cap < window {
		window = cap
	}
	reserve := 0
	if r := s.settings.CompactRatio; r > 0 && r < 1 {
		reserve = window - int(float64(window)*r)
	}
	// Agent.SetContextWindow propagates to the ContextEngine (it implements
	// agentcore.ContextWindowSetter); only the reserve still needs a direct call.
	s.agent.SetContextWindow(window)
	s.mu.Lock()
	s.settings.ContextWindow = window
	cm := s.contextManager
	s.mu.Unlock()
	if engine, ok := cm.(*agentctx.ContextEngine); ok {
		engine.SetReserveTokens(reserve)
	}
}

func (s *Session) SetThinkingLevel(level agentcore.ThinkingLevel) {
	resolved, ok := s.resolveThinkingLevel(string(level))
	if !ok {
		s.emit(SessionEvent{
			Type:  SEError,
			Error: fmt.Errorf("unsupported reasoning_effort %q", string(level)),
		})
		return
	}
	level = agentcore.ThinkingLevel(resolved)

	s.agent.SetThinkingLevel(level)

	s.mu.Lock()
	store := s.store
	s.settings.ReasoningEffort = string(level)
	s.mu.Unlock()

	if store != nil {
		if err := store.AppendReasoningEffortChange(string(level)); err != nil {
			s.emit(SessionEvent{
				Type:  SEError,
				Error: fmt.Errorf("persist reasoning effort: %w", err),
			})
		}
	}

	s.emit(SessionEvent{
		Type:  SEReasoningEffortChanged,
		Level: level,
	})

	lvl := string(level)
	if err := config.PatchEffectiveSettings(s.cwd, config.Settings{
		ReasoningEffort: &lvl,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persist reasoning effort setting: %v\n", err)
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

func (s *Session) AvailableThinkingLevels() []string {
	s.mu.Lock()
	model := s.chatModel
	reg := s.registry
	modelName := s.modelName
	s.mu.Unlock()
	if levels := provider.ThinkingLevelsForModel(model); len(levels) > 0 {
		return levels
	}
	if reg != nil {
		return reg.AvailableThinkingLevels(modelName)
	}
	return []string{""}
}

func (s *Session) AvailableThinkingLevelsFor(prov, modelName string) []string {
	s.mu.Lock()
	currentProvider := s.provider
	currentModel := s.modelName
	currentChatModel := s.chatModel
	reg := s.registry
	s.mu.Unlock()

	if strings.EqualFold(prov, currentProvider) && strings.EqualFold(modelName, currentModel) {
		if levels := provider.ThinkingLevelsForModel(currentChatModel); len(levels) > 0 {
			return levels
		}
	}

	provType, err := s.providerType(prov)
	if err == nil {
		apiKey, baseURL := s.resolveCredentials(prov)
		providerExtra := s.resolveProviderExtra(prov)
		model, err := s.createModel(provType, modelName, apiKey, baseURL, providerExtra)
		if err == nil {
			if levels := provider.ThinkingLevelsForModel(model); len(levels) > 0 {
				return levels
			}
		}
	}

	if reg != nil {
		return reg.AvailableThinkingLevels(modelName)
	}
	return []string{""}
}

func (s *Session) resolveThinkingLevel(level string) (string, bool) {
	s.mu.Lock()
	model := s.chatModel
	reg := s.registry
	modelName := s.modelName
	s.mu.Unlock()
	if levels := provider.ThinkingLevelsForModel(model); len(levels) > 0 {
		return provider.ResolveThinkingLevel(model, level)
	}
	if reg != nil {
		return provider.ClampThinkingLevel(level, reg.AvailableThinkingLevels(modelName))
	}
	return provider.ResolveThinkingLevel(nil, level)
}

func resolveThinkingLevelForModelStrict(model agentcore.ChatModel, level string) (string, bool) {
	if levels := provider.ThinkingLevelsForModel(model); len(levels) == 0 {
		return provider.ResolveThinkingLevel(nil, level)
	}
	return provider.ResolveThinkingLevel(model, level)
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

// Reset closes the current session log and starts a fresh session in the same cwd.
// The in-memory conversation and harness state are cleared; the previous file is flushed.
func (s *Session) Reset() error {
	s.mu.Lock()
	oldStore := s.store
	mgr := s.mgr
	cwd := s.cwd
	s.generation++ // bump early so stale goroutines bail out
	s.mu.Unlock()

	newStore, err := mgr.Create(cwd)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	s.agent.ClearMessages()
	s.agent.ClearAllQueues()
	s.agent.SetThinkingLevel("")
	// Re-point the cache routing key: a fresh session is a new conversation
	// and must not inherit the previous one's cache lineage.
	s.agent.SetPromptCacheKey(newStore.Header().SessionID)

	s.mu.Lock()
	s.store = newStore
	s.autoNamed = false
	s.settings.ReasoningEffort = ""
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
	curChatModel := s.chatModel
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
	targetExtra := s.resolveProviderExtra(targetProvider)

	restoredModel := curChatModel
	if snapshot.Model != "" || snapshot.Provider != "" {
		targetType, err := s.providerType(targetProvider)
		if err != nil {
			return err
		}
		restoredModel, err = s.createModel(targetType, targetModel, targetKey, targetBase, targetExtra)
		if err != nil {
			return fmt.Errorf("restore model %s/%s: %w", targetProvider, targetModel, err)
		}
	}

	// Bump generation early so stale async goroutines bail out.
	s.mu.Lock()
	s.generation++
	s.mu.Unlock()

	s.agent.ClearMessages()
	s.agent.ClearAllQueues()
	// Re-point the cache routing key to the target session so its requests
	// rejoin that conversation's cache lineage (warm if recently active).
	s.agent.SetPromptCacheKey(newStore.Header().SessionID)
	if len(snapshot.Messages) > 0 {
		if err := s.agent.SetMessages(snapshot.Messages); err != nil {
			return fmt.Errorf("restore messages: %w", err)
		}
		agentcore.ReactivateDeferred(s.allTools, snapshot.Messages)
	}
	restoredThinking := ""
	if snapshot.ReasoningEffort != "" {
		var ok bool
		restoredThinking, ok = resolveThinkingLevelForModelStrict(restoredModel, snapshot.ReasoningEffort)
		if !ok {
			return fmt.Errorf("restore reasoning_effort from session: unsupported reasoning_effort %q", snapshot.ReasoningEffort)
		}
		s.agent.SetThinkingLevel(agentcore.ThinkingLevel(restoredThinking))
	} else {
		s.agent.SetThinkingLevel("")
	}
	if restoredModel != nil {
		s.agent.SetModel(restoredModel)
	}

	s.mu.Lock()
	s.store = newStore
	s.provider = targetProvider
	s.modelName = targetModel
	s.autoNamed = newStore.Header().Name != ""
	if snapshot.ReasoningEffort != "" {
		s.settings.ReasoningEffort = restoredThinking
	} else {
		s.settings.ReasoningEffort = ""
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

func (s *Session) CurrentSnapshot() (storage.ContextSnapshot, error) {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store == nil {
		return storage.ContextSnapshot{}, fmt.Errorf("session store is not available")
	}
	return store.BuildSnapshot()
}

func (s *Session) AppendGoalState(entry storage.GoalStateEntry) error {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store == nil {
		return fmt.Errorf("session store is not available")
	}
	return store.AppendGoalState(entry)
}

func (s *Session) Settings() config.Resolved {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

func (s *Session) ContextUsage() *agentcore.ContextUsage {
	return s.agent.ContextUsage()
}

func (s *Session) ContextSnapshot() (*agentcore.ContextSnapshot, bool) {
	if s.contextManager == nil {
		return nil, false
	}
	snapshot := s.contextManager.Snapshot()
	if snapshot == nil {
		return nil, false
	}
	return snapshot, true
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

// CacheStats reports session-cumulative prompt cache metrics. Input includes
// CacheRead per litellm convention; HitRate is CacheRead / Input. SavedUSD
// estimates the dollars saved by serving CacheRead tokens at the cache-read
// rate instead of the full input rate.
type CacheStats struct {
	Input       int
	ReadTokens  int
	WriteTokens int
	HitRate     float64
	SavedUSD    float64
}

func (s *Session) CacheStats() CacheStats {
	usage := s.agent.TotalUsage()
	cs := CacheStats{
		Input:       usage.Input,
		ReadTokens:  usage.CacheRead,
		WriteTokens: usage.CacheWrite,
	}
	if usage.Input > 0 {
		cs.HitRate = float64(usage.CacheRead) / float64(usage.Input)
	}

	s.mu.Lock()
	model := s.modelName
	reg := s.registry
	s.mu.Unlock()

	if reg != nil {
		inRate, _, crRate, _ := reg.CostRates(model)
		if inRate > crRate {
			cs.SavedUSD = float64(usage.CacheRead) * (inRate - crRate) / 1e6
		}
	}
	return cs
}

func (s *Session) CostEstimate() (inputTokens, outputTokens int, cost float64) {
	usage := s.agent.TotalUsage()

	// Input already includes CacheRead per the litellm convention; subtract
	// it so the cached portion is only billed at the cache-read rate, not
	// twice (once at full input rate, once at cache-read rate).
	nonCachedInput := usage.Input - usage.CacheRead
	if nonCachedInput < 0 {
		nonCachedInput = usage.Input
	}

	inputTokens = nonCachedInput + usage.CacheRead + usage.CacheWrite
	outputTokens = usage.Output

	s.mu.Lock()
	model := s.modelName
	reg := s.registry
	s.mu.Unlock()

	if reg != nil {
		inRate, outRate, crRate, cwRate := reg.CostRates(model)
		cost = float64(nonCachedInput)*inRate/1e6 +
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
	snapshotter := s.snapshotter
	s.mu.Unlock()
	if snapshotter != nil {
		snapshotter.Close()
	}
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

// wrapHookContext wraps UserPromptSubmit hook output as a system reminder so
// the model treats it as injected context rather than user-authored text.
func wrapHookContext(text string) string {
	return "<system-reminder>\n" + text + "\n</system-reminder>"
}

// ephemeralQuery sends a one-shot query to the current model using the full
// conversation context. Tool-related blocks are stripped from messages
// (assistant tool_call blocks + tool_result messages) so a thinking-only
// assistant from the parent doesn't arrive empty and get rejected by OpenAI.
//
// The tools array itself IS forwarded verbatim — even though we don't expect
// the model to call any of them — because Anthropic's prompt-cache prefix is
// `tools → system → messages`, and the cache breakpoint we set sits on the
// last system block. Omitting tools here breaks the prefix even though the
// system blocks are byte-identical to the main loop, costing the entire
// system-block cache (~10K tokens) on every /btw and post-turn suggestion.
// The model is steered away from tool use by the suggestion/question prompt
// rather than by tool_choice, because tool_choice "none" is wire-different
// between OpenAI (bare string) and Anthropic (object) and litellm does not
// normalise. If a model ignores the prompt and tries to call a tool we just
// discard the tool_call block from the response — the cache savings outweigh
// the rare false call.
//
// The result is never persisted.
//
// Both SideQuestion (/btw) and GenerateSuggestion share this path.
func (s *Session) ephemeralQuery(ctx context.Context, userText string, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	s.mu.Lock()
	model := s.chatModel
	sessionID := ""
	if s.store != nil {
		sessionID = s.store.Header().SessionID
	}
	s.mu.Unlock()

	if model == nil {
		return nil, fmt.Errorf("no model configured")
	}

	raw, err := s.agent.BuildLLMMessages()
	if err != nil {
		return nil, fmt.Errorf("build llm messages: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("no conversation context")
	}

	// Drop tool and thinking blocks: the litellm bridge serializes assistant
	// via TextContent() which ignores thinking, so a thinking-only assistant
	// would arrive empty and get rejected by OpenAI.
	msgs := make([]agentcore.Message, 0, len(raw))
	for _, m := range raw {
		switch m.Role {
		case agentcore.RoleSystem, agentcore.RoleUser:
			msgs = append(msgs, m)
		case agentcore.RoleAssistant:
			var textBlocks []agentcore.ContentBlock
			for _, b := range m.Content {
				if b.Type == agentcore.ContentText {
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

	// Same tools the main loop sent on its last turn — keeping the `tools`
	// field byte-identical preserves the prompt-cache prefix up to the
	// system-block breakpoint. See the docstring for why we don't also set
	// tool_choice: "none".
	toolSpecs := s.agent.BuildLLMTools()
	// Same routing key as the main loop, for the same reason as the tools
	// forwarding above: on key-routed providers (OpenAI prompt_cache_key)
	// the side query only hits the main conversation's cache when it lands
	// on the same shard.
	defaults := []agentcore.CallOption{agentcore.WithThinking(agentcore.ThinkingOff)}
	if sessionID != "" {
		defaults = append(defaults, agentcore.WithCallPromptCacheKey(sessionID))
	}
	return model.Generate(ctx, msgs, toolSpecs, append(defaults, opts...)...)
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
