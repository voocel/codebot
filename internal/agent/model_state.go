package agent

import (
	"fmt"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/provider"
)

// modelSink is the narrow slice of the agent that modelState delivers to.
// Both methods carry agentcore's documented purity contract (see SetModel's
// godoc: pure assignment under the agent's leaf lock, no events, no
// callbacks), which is what makes calling them INSIDE modelState.mu safe.
type modelSink interface {
	SetModel(m agentcore.ChatModel)
	SetThinkingLevel(level agentcore.ThinkingLevel)
}

// modelState owns the session's model identity (provider / modelName /
// chatModel), the resolved settings, and the temporary skill-override
// baseline — one lock for all of it, so capture → apply → restore sequences
// are single critical sections. Delivery to the sink happens inside the lock:
// computing under the lock but delivering outside would let two concurrent
// changes interleave as A-compute B-compute B-deliver A-deliver, leaving the
// agent and the session permanently disagreeing about the active model.
//
// Methods never reference Session; reg is passed in where needed (immutable
// dep) to keep that property statically checkable.
type modelState struct {
	mu   sync.Mutex
	sink modelSink

	provider  string
	modelName string
	chatModel agentcore.ChatModel
	settings  config.Resolved

	override skillOverride
}

// skillOverride is the pre-skill baseline captured when a skill temporarily
// swaps the model or thinking level; restoreBaseline reinstates it when the
// skill's turn ends.
type skillOverride struct {
	active        bool
	baseProvider  string
	baseModel     string
	baseChatModel agentcore.ChatModel
	baseThinking  string
}

func (m *modelState) current() (prov, name string, chatModel agentcore.ChatModel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.provider, m.modelName, m.chatModel
}

// currentSettings returns a copy. settings.Provider/Model keep their boot-time
// values on purpose — swap deliberately does not rewrite them.
func (m *modelState) currentSettings() config.Resolved {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings
}

// swap installs a user-initiated model/thinking change (/model switch, session
// switch). While a skill override is active the baseline is rewritten too: an
// explicit user choice must survive the skill's end — restoreBaseline would
// otherwise roll the model back to the pre-skill one.
// chatModel may be nil (SwitchSession with nothing to restore): fields update,
// sink.SetModel is skipped. smallModel == "" leaves settings.SmallModel as is.
func (m *modelState) swap(prov, name string, chatModel agentcore.ChatModel, thinking, smallModel string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.provider = prov
	m.modelName = name
	m.chatModel = chatModel
	m.settings.ReasoningEffort = thinking
	if smallModel != "" {
		m.settings.SmallModel = smallModel
	}
	if m.override.active {
		m.override.baseProvider = prov
		m.override.baseModel = name
		m.override.baseChatModel = chatModel
		m.override.baseThinking = thinking
	}
	if chatModel != nil {
		m.sink.SetModel(chatModel)
	}
	m.sink.SetThinkingLevel(agentcore.ThinkingLevel(thinking))
}

// setThinking applies an already-resolved thinking change. Same baseline rule
// as swap: an explicit user change survives an active skill override.
func (m *modelState) setThinking(level string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings.ReasoningEffort = level
	if m.override.active {
		m.override.baseThinking = level
	}
	m.sink.SetThinkingLevel(agentcore.ThinkingLevel(level))
}

// overrideForSkill captures the baseline (first override only) and installs
// the skill's model. The thinking level is re-resolved against the incoming
// model inside the same critical section — resolving it outside would open a
// window where a concurrent change lands between capture and apply and gets
// recorded as the "user's" baseline.
func (m *modelState) overrideForSkill(prov, name string, chatModel agentcore.ChatModel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	effective, ok := resolveThinkingLevelForModelStrict(chatModel, m.settings.ReasoningEffort)
	if !ok {
		return fmt.Errorf("unsupported reasoning_effort %q", m.settings.ReasoningEffort)
	}
	m.captureBaselineLocked()
	m.provider = prov
	m.modelName = name
	m.chatModel = chatModel
	m.settings.ReasoningEffort = effective
	m.sink.SetModel(chatModel)
	m.sink.SetThinkingLevel(agentcore.ThinkingLevel(effective))
	return nil
}

// overrideThinkingForSkill applies a skill's thinking level, capturing the
// baseline in the same critical section.
func (m *modelState) overrideThinkingForSkill(level string, reg *provider.ModelRegistry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	resolved, ok := resolveThinkingAgainst(m.chatModel, m.modelName, reg, level)
	if !ok {
		return fmt.Errorf("unsupported reasoning_effort %q", level)
	}
	m.captureBaselineLocked()
	m.settings.ReasoningEffort = resolved
	m.sink.SetThinkingLevel(agentcore.ThinkingLevel(resolved))
	return nil
}

func (m *modelState) captureBaselineLocked() {
	if m.override.active {
		return
	}
	m.override = skillOverride{
		active:        true,
		baseProvider:  m.provider,
		baseModel:     m.modelName,
		baseChatModel: m.chatModel,
		baseThinking:  m.settings.ReasoningEffort,
	}
}

// restoreBaseline reverts a temporary skill override. A baseline thinking
// level that no longer resolves against the restored model degrades to ""
// (reported via the return for the caller to surface) — the model/provider
// restore itself must never be skipped, or the baseline is lost for good.
func (m *modelState) restoreBaseline() (unsupported string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.override.active {
		return ""
	}
	base := m.override
	m.override = skillOverride{}
	m.provider = base.baseProvider
	m.modelName = base.baseModel
	m.chatModel = base.baseChatModel
	effective, ok := resolveThinkingLevelForModelStrict(base.baseChatModel, base.baseThinking)
	if !ok {
		effective = ""
		unsupported = base.baseThinking
	}
	m.settings.ReasoningEffort = effective
	if base.baseChatModel != nil {
		m.sink.SetModel(base.baseChatModel)
	}
	m.sink.SetThinkingLevel(agentcore.ThinkingLevel(effective))
	return unsupported
}

// clearOverride drops the baseline without delivering anything — Reset and
// SwitchSession install their target model/thinking explicitly themselves.
func (m *modelState) clearOverride() {
	m.mu.Lock()
	m.override = skillOverride{}
	m.mu.Unlock()
}

// applyContextWindow stores capped model limits for propagation outside the lock.
func (m *modelState) applyContextWindow(window, maxOutput int) (applied, reserve int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cap := m.settings.CompactWindow; cap > 0 && cap < window {
		window = cap
	}
	m.settings.ContextWindow = window
	m.settings.MaxOutputTokens = maxOutput
	return window, m.settings.CompactReserveTokens()
}

// resolveThinkingAgainst resolves level against a live model, falling back to
// the registry's levels for the model name. Pure function shared by the locked
// modelState methods and Session.resolveThinkingLevel.
func resolveThinkingAgainst(model agentcore.ChatModel, modelName string, reg *provider.ModelRegistry, level string) (string, bool) {
	if levels := provider.ThinkingLevelsForModel(model); len(levels) > 0 {
		return provider.ResolveThinkingLevel(model, level)
	}
	if reg != nil {
		return provider.ClampThinkingLevel(level, reg.AvailableThinkingLevels(modelName))
	}
	return provider.ResolveThinkingLevel(nil, level)
}
