package agent

import (
	"context"
	"errors"
	"fmt"
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

// baseRunCtx is the base context for every agent-loop entry (Prompt / Continue /
// idle resume). It carries the session cwd as a LIVE source (WithCwdFunc), so a
// worktree switch mid-turn (RetargetWorkspace) is seen by the next tool call —
// same-turn edits land in the new workspace, without rebuilding any tools.
// Teammates capture a fixed cwd at spawn (see teammateCwd), so the live source
// never bleeds across them.
func (s *Session) baseRunCtx() context.Context {
	return tools.WithCwdFunc(context.Background(), s.currentCwd)
}

func (s *Session) startPromptMessages(msgs ...agentcore.AgentMessage) error {
	ctx, run, previous := s.beginTelemetryRun(s.baseRunCtx())
	if err := s.deps.agent.PromptMessages(ctx, msgs...); err != nil {
		s.rollbackTelemetryRun(run, previous, err)
		return err
	}
	s.commitTelemetryRun(previous)
	return nil
}

func (s *Session) startContinue() error {
	ctx, run, previous := s.beginTelemetryRun(s.baseRunCtx())
	if err := s.deps.agent.Continue(ctx); err != nil {
		s.rollbackTelemetryRun(run, previous, err)
		return err
	}
	s.commitTelemetryRun(previous)
	return nil
}

func (s *Session) beginTelemetryRun(ctx context.Context) (context.Context, *telemetry.Run, *telemetry.Run) {
	if s.deps.telemetryTracer == nil {
		return ctx, nil, nil
	}
	ctx, run := s.deps.telemetryTracer.StartRun(ctx, "agent run")
	previous := s.run.beginRun(run)
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
	s.run.rollbackRun(run, previous)
	run.End(err)
}

func (s *Session) endTelemetryRun(err error) {
	if run := s.run.endRun(); run != nil {
		run.End(err)
	}
}

func (s *Session) Prompt(text string) error {
	var hookContext string
	if s.deps.hookRunner != nil {
		dec, err := s.deps.hookRunner.RunUserPromptSubmit(context.Background(), text)
		if err != nil {
			return err
		}
		hookContext = strings.TrimSpace(dec.AdditionalContext)
	}
	s.beginTurn()
	if fn := s.hooks.getBeforePrompt(); fn != nil {
		fn()
	}
	if hookContext != "" {
		s.queueRuntimeReminder("hook_context", ReminderHookContext, wrapHookContext(hookContext))
	}
	s.runtime.beforeUserPrompt([]agentcore.ContentBlock{agentcore.TextBlock(text)})

	var msgs []agentcore.AgentMessage
	if preamble, ok := s.prompt.takePreamble(); ok {
		msgs = append(msgs, injectedUserMsg(preamble))
	}
	msgs = append(msgs, s.buildUserMessage(agentcore.TextBlock(text)))
	return s.startPromptMessages(msgs...)
}

func (s *Session) PromptWithBlocks(blocks []agentcore.ContentBlock) error {
	s.beginTurn()
	if fn := s.hooks.getBeforePrompt(); fn != nil {
		fn()
	}
	s.runtime.beforeUserPrompt(blocks)

	var msgs []agentcore.AgentMessage
	if preamble, ok := s.prompt.takePreamble(); ok {
		msgs = append(msgs, injectedUserMsg(preamble))
	}
	msgs = append(msgs, s.buildUserMessage(blocks...))
	return s.startPromptMessages(msgs...)
}

// buildUserMessage creates a user message with reminders prepended as text blocks.
func (s *Session) buildUserMessage(userBlocks ...agentcore.ContentBlock) agentcore.Message {
	recallReminders := s.memoryRecallReminders(userBlocks)
	runtimeReminders, staticReminders := s.reminders.drainForPrompt()

	if len(runtimeReminders) == 0 && len(staticReminders) == 0 && len(recallReminders) == 0 {
		return agentcore.Message{
			Role:      agentcore.RoleUser,
			Content:   userBlocks,
			Timestamp: time.Now(),
		}
	}

	blocks := make([]agentcore.ContentBlock, 0, len(runtimeReminders)+len(staticReminders)+len(recallReminders)+len(userBlocks))
	for _, r := range runtimeReminders {
		blocks = append(blocks, agentcore.TextBlock(r))
	}
	for _, r := range staticReminders {
		blocks = append(blocks, agentcore.TextBlock(r))
	}
	// Recalled memories sit closest to the user text — the freshest, most
	// specific context right next to the question it relates to.
	for _, r := range recallReminders {
		blocks = append(blocks, agentcore.TextBlock(r))
	}
	blocks = append(blocks, userBlocks...)
	return agentcore.Message{
		Role:      agentcore.RoleUser,
		Content:   blocks,
		Timestamp: time.Now(),
	}
}

const (
	memoryRecallMaxFiles     = 3         // files surfaced per prompt
	memoryRecallSessionBytes = 60 * 1024 // recall budget per context window
)

// memoryRecallReminders surfaces auto-memory topic files relevant to the
// user's message as system reminders. Recall is lexical (no model call), so
// it runs synchronously on the prompt path; a session-scoped dedup set and
// byte budget keep repeated prompts from re-injecting the same files. Both
// reset when the context window resets (clear, reset, compaction).
func (s *Session) memoryRecallReminders(userBlocks []agentcore.ContentBlock) []string {
	var text string
	for _, b := range userBlocks {
		if b.Type == agentcore.ContentText && b.Text != "" {
			text = b.Text
		}
	}
	dir := s.prompt.memoryDir()

	exclude, budgetLeft := s.reminders.recallBudget()
	if dir == "" || text == "" || budgetLeft <= 0 {
		return nil
	}

	recalls := config.RecallMemories(dir, text, exclude, memoryRecallMaxFiles)
	if len(recalls) == 0 {
		return nil
	}
	out := make([]string, 0, len(recalls))
	for _, r := range recalls {
		if !s.reminders.chargeRecall(r.Path, len(r.Content)) {
			break
		}
		out = append(out, config.FormatMemoryRecallReminder(r))
	}
	return out
}

// resetMemoryRecall clears the recall dedup set and budget — call whenever
// the context window is rebuilt and previously injected memories are gone.
func (s *Session) resetMemoryRecall() {
	s.reminders.resetRecall()
}

func (s *Session) queueRuntimeReminder(key string, kind RuntimeReminderKind, reminder string) {
	if reminder == "" {
		return
	}

	if !s.reminders.queue(key, reminder) {
		return
	}
	s.metrics.recordReminder(kind)
	s.turn.recordReminderSnapshot(kind, "next_prompt")

	s.emit(SessionEvent{
		Type:         SERuntimeReminder,
		Reminder:     reminder,
		ReminderKind: kind,
	})
}

// continueIfCurrentGeneration re-checks the generation and starts the
// continuation in one critical section, so the check-then-act cannot straddle
// a Reset/SwitchSession teardown: mid-switch the agent's run lifecycle is
// held (Continue fails fast with ErrRunsHeld), and a completed switch flips
// the generation. s.mu → agent lock is acyclic — the agent never calls back
// into the Session under its own lock (Subscribe lifecycle contract).
func (s *Session) continueIfCurrentGeneration(gen uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != gen {
		return errStaleSessionGeneration
	}
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
	if !s.deps.agent.State().IsRunning {
		return false
	}

	// Already steered this turn: the reminder is in flight, report it as
	// delivered so the caller does not fall through to another path.
	if !s.reminders.markSteered(key) {
		return true
	}

	s.metrics.recordReminder(kind)
	s.turn.recordReminderSnapshot(kind, "steer")
	s.deps.agent.Steer(injectedUserMsg(reminder))
	s.emit(SessionEvent{
		Type:         SERuntimeReminder,
		Reminder:     reminder,
		ReminderKind: kind,
	})
	return true
}

func (s *Session) tryAutoResumeRuntimeReminder(key string, kind RuntimeReminderKind, reminder string) bool {
	if s.deps.agent.State().IsRunning {
		return false
	}

	msgs := s.deps.agent.Messages()
	if len(msgs) == 0 || msgs[len(msgs)-1].GetRole() != agentcore.RoleAssistant {
		return false
	}

	if s.reminders.autoResumed(key) {
		return false
	}

	ctx, run, previous := s.beginTelemetryRun(s.baseRunCtx())
	result, err := s.deps.agent.Inject(ctx, injectedUserMsg(reminder))
	if err != nil {
		// Only ErrRunsHeld can land here (a Reset/SwitchSession holds the run
		// lifecycle) and it queues nothing, so falling through to next-prompt
		// queueing delivers the reminder exactly once.
		s.rollbackTelemetryRun(run, previous, err)
		return false
	}
	if result.Disposition != agentcore.InjectResumedIdleRun {
		// A run started between our idle check and the inject: the reminder
		// was steered into it (or queued for the next launch) — delivered
		// either way. Falling back to next-prompt queueing here would deliver
		// it twice. No run was started by us, so roll the telemetry run back.
		s.rollbackTelemetryRun(run, previous, nil)
		s.metrics.recordReminder(kind)
		s.turn.recordReminderSnapshot(kind, "steer")
		s.emit(SessionEvent{
			Type:         SERuntimeReminder,
			Reminder:     reminder,
			ReminderKind: kind,
		})
		return true
	}
	s.commitTelemetryRun(previous)

	s.reminders.markAutoResumed(key)

	s.metrics.recordReminder(kind)
	s.turn.recordReminderSnapshot(kind, "auto_continue")
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
	s.hooks.setPlanModeSignal(fn)
}

func (s *Session) currentPlanModeSignal() PlanModeSignal {
	fn := s.hooks.getPlanModeSignal()
	if fn == nil {
		return PlanModeSignal{}
	}
	return fn()
}

// SetGoalSignal registers a callback the runtime polls at natural stop points
// to decide whether an explicit /goal should auto-continue.
func (s *Session) SetGoalSignal(fn func() goal.Signal) {
	s.hooks.setGoalSignal(fn)
}

func (s *Session) currentGoalSignal() goal.Signal {
	fn := s.hooks.getGoalSignal()
	if fn == nil {
		return goal.Signal{}
	}
	return fn()
}

func (s *Session) SetGoalUsageLimitHandler(fn func(string) (goal.State, error)) {
	s.hooks.setGoalUsageLimit(fn)
}

func (s *Session) markGoalUsageLimited(reason string) error {
	fn := s.hooks.getGoalUsageLimit()
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
	s.hooks.setBeforePrompt(fn)
}

// SetIdleHook registers a callback invoked after each turn fully settles
// (EventAgentEnd with no pending automatic continuation).
// Must be cheap on its fast path — it runs on the event dispatch goroutine.
func (s *Session) SetIdleHook(fn func()) {
	s.hooks.setIdleHook(fn)
}

func (s *Session) Steer(text string) {
	s.deps.agent.Steer(agentcore.UserMsg(text))
}

// EnqueueBackgroundResult delivers a completed background task without
// interrupting an active run. If the session is idle, it starts a continuation
// so the result does not wait for another user prompt.
func (s *Session) EnqueueBackgroundResult(msg agentcore.AgentMessage) {
	s.deps.agent.FollowUp(msg)
	s.continuePendingBackgroundResult()
}

func (s *Session) continuePendingBackgroundResult() bool {
	if s.deps.agent.State().IsRunning || !s.deps.agent.HasFollowUps() {
		return false
	}

	messages := s.deps.agent.Messages()
	if len(messages) == 0 {
		return false
	}
	last := messages[len(messages)-1]
	if last == nil || last.GetRole() != agentcore.RoleAssistant {
		return false
	}

	s.mu.Lock()
	if s.backgroundWakeSuppressed {
		s.mu.Unlock()
		return false
	}
	gen := s.generation
	s.mu.Unlock()

	return s.resumeBackgroundResult(gen)
}

func (s *Session) resumeBackgroundResult(gen uint64) bool {
	s.mu.Lock()
	if s.generation != gen || s.backgroundWakeSuppressed {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	err := s.continueIfCurrentGeneration(gen)
	if err == nil {
		// Abort may have raced between the check above and Continue. In that
		// ordering the new run did not exist when Abort tried to cancel it.
		s.mu.Lock()
		suppressed := s.backgroundWakeSuppressed
		s.mu.Unlock()
		if suppressed {
			s.deps.agent.AbortSilent()
		}
		return true
	}
	if errors.Is(err, errStaleSessionGeneration) {
		return false
	}
	if errors.Is(err, agentcore.ErrAlreadyRunning) {
		// Another prompt won the idle race and will consume the follow-up at
		// its natural continuation point.
		return true
	}
	if errors.Is(err, agentcore.ErrBadContinuation) && !s.deps.agent.HasFollowUps() {
		// A concurrent continuation already consumed the same queued batch.
		return false
	}
	if errors.Is(err, agentcore.ErrNoMessages) {
		// A concurrent Reset cleared the conversation out from under the
		// queued result; the result died with the old session.
		return false
	}
	if errors.Is(err, agentcore.ErrRunsHeld) {
		// A Reset/SwitchSession holds the run lifecycle. The queued result
		// either dies with the old session (the switch clears the queues) or
		// is consumed by the next natural continuation.
		return false
	}

	s.clearSkillDelta()
	s.emit(SessionEvent{
		Type:  SEError,
		Error: fmt.Errorf("background result continue: %w", err),
	})
	return false
}

func (s *Session) Abort() {
	s.mu.Lock()
	s.backgroundWakeSuppressed = true
	s.mu.Unlock()
	s.deps.agent.Abort()
}

func (s *Session) AbortSilent() {
	s.mu.Lock()
	s.backgroundWakeSuppressed = true
	s.mu.Unlock()
	s.deps.agent.AbortSilent()
}

func (s *Session) IsRunning() bool {
	return s.deps.agent.State().IsRunning
}

func (s *Session) WaitForIdle() {
	s.deps.agent.WaitForIdle()
}

func (s *Session) ClearConversation() {
	// Third holder alongside Reset/SwitchSession: the hold freezes the run
	// lifecycle but does not mutually exclude holders' surgery — switchMu does.
	s.switchMu.Lock()
	defer s.switchMu.Unlock()

	// /clear arrives at an idle boundary, but idle auto-resume can start a
	// run between the caller's idle check and the wipe — hold the lifecycle
	// so the clear cannot race a launch.
	release := s.deps.agent.HoldRuns()
	defer release()
	if err := s.deps.agent.SetMessages(nil); err != nil {
		s.emit(SessionEvent{Type: SEError, Error: fmt.Errorf("clear conversation: %w", err)})
		return
	}
	s.deps.agent.ClearAllQueues()
	// Conversation gone → LLM lost its read history. Drop file-read stamps
	// so the next write/edit forces a fresh read.
	if s.deps.fileReadState != nil {
		s.deps.fileReadState.Reset()
	}
	s.resetMemoryRecall()
	// The deferred-tools preamble was wiped with the history — rearm it so
	// the next prompt re-injects the deferred tool list.
	s.prompt.setPreambleInjected(false)
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
	if err := s.model.overrideForSkill(prov, resolved, chatModel); err != nil {
		return fmt.Errorf("resolve skill model %q: %w", model, err)
	}
	return nil
}

func (s *Session) applyTemporarySkillThinking(level string) error {
	if level == "" {
		return nil
	}
	return s.model.overrideThinkingForSkill(level, s.deps.registry)
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
	if unsupported := s.model.restoreBaseline(); unsupported != "" {
		s.emit(SessionEvent{
			Type:  SEError,
			Error: fmt.Errorf("unsupported reasoning_effort %q", unsupported),
		})
	}
}

func (s *Session) resolveModelOverride(pattern string) (string, string, agentcore.ChatModel, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", "", nil, fmt.Errorf("empty model override")
	}

	curProv, _, _ := s.model.current()

	if strings.Contains(pattern, "/") {
		if prov, model, ok := strings.Cut(pattern, "/"); ok {
			provType, err := s.providerType(prov)
			if err != nil {
				return "", "", nil, err
			}
			apiKey, baseURL := s.resolveCredentials(prov)
			providerExtra := s.resolveProviderExtra(prov)
			chatModel, err := s.deps.createModel(provType, model, apiKey, baseURL, providerExtra)
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
	for provName, pc := range s.deps.providers {
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
		chatModel, err := s.deps.createModel(provType, m.model, apiKey, baseURL, providerExtra)
		return m.provider, m.model, chatModel, err
	case 0:
		provType, err := s.providerType(curProv)
		if err != nil {
			return "", "", nil, err
		}
		apiKey, baseURL := s.resolveCredentials(curProv)
		providerExtra := s.resolveProviderExtra(curProv)
		chatModel, err := s.deps.createModel(provType, pattern, apiKey, baseURL, providerExtra)
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
	providerExtra := s.resolveProviderExtra(prov)
	chatModel, err := s.deps.createModel(provType, model, apiKey, baseURL, providerExtra)
	if err != nil {
		return fmt.Errorf("create model %s/%s: %w", prov, model, err)
	}
	currentThinking := s.Settings().ReasoningEffort
	resolvedThinking, ok := provider.ResolveThinkingLevel(chatModel, currentThinking)
	if !ok {
		return fmt.Errorf("switch model %s/%s: unsupported reasoning_effort %q", prov, model, currentThinking)
	}

	err = s.persist.withStore(func(store *storage.Store) error {
		return store.AppendModelChange(prov, model)
	})
	if err != nil {
		return fmt.Errorf("persist model change: %w", err)
	}

	// Sync small_model: provider config > fallback to current model.
	smallModel := model
	if pc, ok := s.deps.providers[prov]; ok && pc.SmallModel != "" {
		smallModel = pc.SmallModel
	}
	s.model.swap(prov, model, chatModel, resolvedThinking, smallModel)

	s.emit(SessionEvent{
		Type:      SEModelChanged,
		ModelName: model,
		Provider:  prov,
	})

	s.updateContextFromRegistry(prov, model)

	return nil
}

// providerType returns the protocol type for a provider key.
func (s *Session) providerType(prov string) (string, error) {
	if pc, ok := s.deps.providers[prov]; ok {
		return pc.ProviderType(prov)
	}
	return config.ResolveProviderType(prov, "")
}

func (s *Session) resolveProviderExtra(prov string) map[string]any {
	if pc, ok := s.deps.providers[prov]; ok {
		return pc.ProviderExtra()
	}
	return nil
}

// updateContextFromRegistry updates context window from registry metadata if available.
// It tries provider-qualified lookup first (e.g. "anthropic/claude-sonnet-4-5"),
// then falls back to bare modelID for custom providers not in the registry.
func (s *Session) updateContextFromRegistry(providerKey, modelID string) {
	if s.deps.registry == nil {
		return
	}
	// Try provider-qualified lookup using the protocol type.
	provType, err := s.providerType(providerKey)
	if err == nil {
		entry, _, err := s.deps.registry.Resolve(provType + "/" + modelID)
		if err == nil && entry.ContextWindow > 0 {
			s.applyContextWindow(entry.ContextWindow)
			return
		}
	}
	entry, _, err := s.deps.registry.Resolve(modelID)
	if err != nil || entry.ContextWindow <= 0 {
		return
	}
	s.applyContextWindow(entry.ContextWindow)
}

func (s *Session) applyContextWindow(window int) {
	// Re-apply user-configured compaction caps to the new model window so that
	// mid-session model switches honor compact_window / compact_ratio.
	applied, reserve := s.model.applyContextWindow(window)
	// Agent.SetContextWindow propagates to the ContextEngine (it implements
	// agentcore.ContextWindowSetter); only the reserve still needs a direct call.
	s.deps.agent.SetContextWindow(applied)
	if engine, ok := s.deps.contextManager.(*agentctx.ContextEngine); ok {
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

	s.model.setThinking(resolved)

	err := s.persist.withStore(func(store *storage.Store) error {
		return store.AppendReasoningEffortChange(string(level))
	})
	if err != nil {
		s.emit(SessionEvent{
			Type:  SEError,
			Error: fmt.Errorf("persist reasoning effort: %w", err),
		})
	}

	s.emit(SessionEvent{
		Type:  SEReasoningEffortChanged,
		Level: level,
	})

	lvl := string(level)
	if err := config.PatchEffectiveSettings(s.currentCwd(), config.Settings{
		ReasoningEffort: &lvl,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persist reasoning effort setting: %v\n", err)
	}
}

func (s *Session) ModelName() string {
	_, name, _ := s.model.current()
	return name
}

func (s *Session) Provider() string {
	prov, _, _ := s.model.current()
	return prov
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
	_, modelName, model := s.model.current()
	if levels := provider.ThinkingLevelsForModel(model); len(levels) > 0 {
		return levels
	}
	if reg := s.deps.registry; reg != nil {
		return reg.AvailableThinkingLevels(modelName)
	}
	return []string{""}
}

func (s *Session) AvailableThinkingLevelsFor(prov, modelName string) []string {
	currentProvider, currentModel, currentChatModel := s.model.current()
	reg := s.deps.registry

	if strings.EqualFold(prov, currentProvider) && strings.EqualFold(modelName, currentModel) {
		if levels := provider.ThinkingLevelsForModel(currentChatModel); len(levels) > 0 {
			return levels
		}
	}

	provType, err := s.providerType(prov)
	if err == nil {
		apiKey, baseURL := s.resolveCredentials(prov)
		providerExtra := s.resolveProviderExtra(prov)
		model, err := s.deps.createModel(provType, modelName, apiKey, baseURL, providerExtra)
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
	_, modelName, model := s.model.current()
	return resolveThinkingAgainst(model, modelName, s.deps.registry, level)
}

func resolveThinkingLevelForModelStrict(model agentcore.ChatModel, level string) (string, bool) {
	if levels := provider.ThinkingLevelsForModel(model); len(levels) == 0 {
		return provider.ResolveThinkingLevel(nil, level)
	}
	return provider.ResolveThinkingLevel(model, level)
}

// resolveCredentials returns provider credentials from settings only — no
// environment fallback.
func (s *Session) resolveCredentials(prov string) (apiKey, baseURL string) {
	if pc, ok := s.deps.providers[prov]; ok {
		return pc.APIKey, pc.BaseURL
	}
	return "", ""
}

// Reset closes the current session log and starts a fresh session in the same cwd.
// The in-memory conversation and harness state are cleared; the previous file is flushed.
func (s *Session) Reset() error {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()

	// Fallible I/O before the generation bump: a bump followed by an early
	// return would discard pending continuations for a switch that never
	// happened (SwitchSession orders its Open/BuildSnapshot the same way).
	newStore, err := s.deps.mgr.Create(s.currentCwd())
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	s.mu.Lock()
	s.generation++ // bump early so stale goroutines bail out
	s.mu.Unlock()

	// Freeze the run lifecycle for the whole teardown: drains any run a
	// continuation slipped in and rejects new launches (ErrRunsHeld) until
	// the fresh session is fully installed.
	release := s.deps.agent.HoldRuns()
	defer release()

	if err := s.deps.agent.SetMessages(nil); err != nil {
		_ = newStore.Close()
		return fmt.Errorf("clear conversation: %w", err)
	}
	s.deps.agent.ClearAllQueues()
	s.model.setThinking("")
	// Re-point the cache routing key: a fresh session is a new conversation
	// and must not inherit the previous one's cache lineage.
	s.deps.agent.SetPromptCacheKey(newStore.Header().SessionID)

	oldStore := s.persist.swapStore(newStore, false)
	s.resetHarnessState()
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
	s.switchMu.Lock()
	defer s.switchMu.Unlock()

	curProvider, curModel, curChatModel := s.model.current()

	newStore, err := s.deps.mgr.Open(id)
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
		restoredModel, err = s.deps.createModel(targetType, targetModel, targetKey, targetBase, targetExtra)
		if err != nil {
			return fmt.Errorf("restore model %s/%s: %w", targetProvider, targetModel, err)
		}
	}

	// Pure validation, so it belongs with the fallible section above: failing
	// after the teardown starts would leave the agent holding the target
	// history while the store/model still point at the old session.
	restoredThinking := ""
	if snapshot.ReasoningEffort != "" {
		var ok bool
		restoredThinking, ok = resolveThinkingLevelForModelStrict(restoredModel, snapshot.ReasoningEffort)
		if !ok {
			return fmt.Errorf("restore reasoning_effort from session: unsupported reasoning_effort %q", snapshot.ReasoningEffort)
		}
	}

	// Bump generation early so stale async goroutines bail out.
	s.mu.Lock()
	s.generation++
	s.mu.Unlock()

	// Freeze the run lifecycle for the teardown, same as Reset.
	release := s.deps.agent.HoldRuns()
	defer release()

	if err := s.deps.agent.SetMessages(nil); err != nil {
		return fmt.Errorf("clear conversation: %w", err)
	}
	s.deps.agent.ClearAllQueues()
	// Re-point the cache routing key to the target session so its requests
	// rejoin that conversation's cache lineage (warm if recently active).
	s.deps.agent.SetPromptCacheKey(newStore.Header().SessionID)
	if len(snapshot.Messages) > 0 {
		if err := s.deps.agent.SetMessages(snapshot.Messages); err != nil {
			return fmt.Errorf("restore messages: %w", err)
		}
		agentcore.ReactivateDeferred(s.prompt.allToolsSnapshot(), snapshot.Messages)
	}
	// swap keeps session fields and agent in step atomically; a stale
	// chatModel here would hand the old session's model to ephemeralQuery,
	// AvailableThinkingLevels, and the skill-override baseline capture.
	s.model.swap(targetProvider, targetModel, restoredModel, restoredThinking, "")

	oldStore := s.persist.swapStore(newStore, newStore.Header().Name != "")
	s.resetHarnessState()
	// Derive from the restored history — same heuristic bootstrap uses on
	// resume (assemble_runtime.go): any restored messages imply the preamble
	// is already in the conversation; re-injecting would duplicate it.
	s.prompt.setPreambleInjected(len(snapshot.Messages) > 0)

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
	store := s.persist.currentStore()
	if store == nil {
		return storage.ContextSnapshot{}, fmt.Errorf("session store is not available")
	}
	return store.BuildSnapshot()
}

func (s *Session) AppendGoalState(entry storage.GoalStateEntry) error {
	store := s.persist.currentStore()
	if store == nil {
		return fmt.Errorf("session store is not available")
	}
	return store.AppendGoalState(entry)
}

func (s *Session) Settings() config.Resolved {
	return s.model.currentSettings()
}

func (s *Session) ContextUsage() *agentcore.ContextUsage {
	return s.deps.agent.ContextUsage()
}

func (s *Session) ContextSnapshot() (*agentcore.ContextSnapshot, bool) {
	if s.deps.contextManager == nil {
		return nil, false
	}
	snapshot := s.deps.contextManager.Snapshot()
	if snapshot == nil {
		return nil, false
	}
	return snapshot, true
}

func (s *Session) RecentToolCalls(limit int) []ToolCallSnapshot {
	recent := s.turn.recentSnapshot(limit)
	snapshots := make([]ToolCallSnapshot, 0, len(recent))
	for _, call := range recent {
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
	return s.turn.lastReminderSnapshot()
}

func (s *Session) LastCompaction() (CompactionSnapshot, bool) {
	return s.metrics.lastCompactionSnapshot()
}

func (s *Session) LastRunSummary() (agentcore.RunSummary, bool) {
	return s.turn.runSummarySnapshot()
}

func (s *Session) TotalTokens() int {
	return s.deps.agent.TotalUsage().TotalTokens
}

func (s *Session) Registry() *provider.ModelRegistry {
	return s.deps.registry
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
	usage := s.deps.agent.TotalUsage()
	cs := CacheStats{
		Input:       usage.Input,
		ReadTokens:  usage.CacheRead,
		WriteTokens: usage.CacheWrite,
	}
	if usage.Input > 0 {
		cs.HitRate = float64(usage.CacheRead) / float64(usage.Input)
	}

	_, model, _ := s.model.current()
	if reg := s.deps.registry; reg != nil {
		inRate, _, crRate, _ := reg.CostRates(model)
		if inRate > crRate {
			cs.SavedUSD = float64(usage.CacheRead) * (inRate - crRate) / 1e6
		}
	}
	return cs
}

func (s *Session) CostEstimate() (inputTokens, outputTokens int, cost float64) {
	usage := s.deps.agent.TotalUsage()

	// Input already includes CacheRead per the litellm convention; subtract
	// it so the cached portion is only billed at the cache-read rate, not
	// twice (once at full input rate, once at cache-read rate).
	nonCachedInput := usage.Input - usage.CacheRead
	if nonCachedInput < 0 {
		nonCachedInput = usage.Input
	}

	inputTokens = nonCachedInput + usage.CacheRead + usage.CacheWrite
	outputTokens = usage.Output

	_, model, _ := s.model.current()
	if reg := s.deps.registry; reg != nil {
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
	return s.deps.agent.Messages()
}

func (s *Session) LastAssistantText() string {
	msgs := s.deps.agent.Messages()
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
	store := s.persist.currentStore()
	if store == nil {
		return ""
	}
	return store.Header().SessionID
}

func (s *Session) CurrentSessionInfo() (storage.SessionInfo, error) {
	store := s.persist.currentStore()
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
	mgr := s.deps.mgr
	s.mu.Unlock()

	if mgr == nil {
		return nil, fmt.Errorf("no session manager configured")
	}
	return mgr.List()
}

func (s *Session) Close() {
	s.AbortSilent()
	s.deps.agent.WaitForIdle()
	if s.deps.unsub != nil {
		s.deps.unsub()
	}
	if err := s.persist.flushPending(); err != nil {
		s.emit(SessionEvent{
			Type:  SEError,
			Error: err,
		})
	}
	if s.deps.snapshotter != nil {
		s.deps.snapshotter.Close()
	}
	if store := s.persist.currentStore(); store != nil {
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
	// Cross-group snapshot (model vs store): a session switch between the two
	// reads could pair the new model with the old store's cache key, costing
	// one cache miss on an ephemeral call — accepted.
	_, _, model := s.model.current()
	sessionID := ""
	if store := s.persist.currentStore(); store != nil {
		sessionID = store.Header().SessionID
	}

	if model == nil {
		return nil, fmt.Errorf("no model configured")
	}

	raw, err := s.deps.agent.BuildLLMMessages()
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
	toolSpecs := s.deps.agent.BuildLLMTools()
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
