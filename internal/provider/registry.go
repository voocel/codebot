package provider

import (
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
)

// ModelEntry describes a known LLM model.
type ModelEntry struct {
	Provider      string
	ID            string
	Name          string
	ContextWindow int
	MaxTokens     int
	Reasoning     bool // supports thinking/reasoning
}

// ModelRegistry holds known models and provides resolution/cycling.
type ModelRegistry struct {
	models []ModelEntry
}

// NewModelRegistry creates a registry pre-loaded with built-in models.
func NewModelRegistry() *ModelRegistry {
	r := &ModelRegistry{}
	r.loadBuiltins()
	return r
}

// Resolve parses a model pattern and returns the matched entry and optional thinking level.
// Pattern formats:
//   - "claude-sonnet-4-5"                  → exact/partial match
//   - "anthropic/claude-sonnet-4-5"        → provider/model
//   - "claude-sonnet-4-5:high"             → model + thinking level
//   - "sonnet"                             → partial match
func (r *ModelRegistry) Resolve(pattern string) (*ModelEntry, agentcore.ThinkingLevel, error) {
	if pattern == "" {
		return nil, "", fmt.Errorf("empty model pattern")
	}

	// Extract thinking level suffix (e.g. "model:high")
	thinkingLevel := agentcore.ThinkingLevel("")
	if idx := strings.LastIndex(pattern, ":"); idx > 0 {
		suffix := pattern[idx+1:]
		if isValidThinkingLevel(suffix) {
			thinkingLevel = agentcore.ThinkingLevel(suffix)
			pattern = pattern[:idx]
		}
	}

	// Try provider/model format
	if idx := strings.Index(pattern, "/"); idx > 0 {
		prov := pattern[:idx]
		modelID := pattern[idx+1:]
		for i := range r.models {
			if strings.EqualFold(r.models[i].Provider, prov) &&
				strings.EqualFold(r.models[i].ID, modelID) {
				return &r.models[i], thinkingLevel, nil
			}
		}
	}

	// Try exact ID match
	for i := range r.models {
		if strings.EqualFold(r.models[i].ID, pattern) {
			return &r.models[i], thinkingLevel, nil
		}
	}

	// Try partial match (ID or Name contains pattern)
	lower := strings.ToLower(pattern)
	var candidates []int
	for i := range r.models {
		if strings.Contains(strings.ToLower(r.models[i].ID), lower) ||
			strings.Contains(strings.ToLower(r.models[i].Name), lower) {
			candidates = append(candidates, i)
		}
	}

	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("no model matching %q", pattern)
	}

	// Prefer non-dated versions (aliases) over dated ones.
	best := candidates[0]
	for _, idx := range candidates[1:] {
		if !hasDatedSuffix(r.models[idx].ID) && hasDatedSuffix(r.models[best].ID) {
			best = idx
		}
	}
	return &r.models[best], thinkingLevel, nil
}

// List returns models matching the filter (empty filter = all).
func (r *ModelRegistry) List(filter string) []ModelEntry {
	if filter == "" {
		return append([]ModelEntry{}, r.models...)
	}
	lower := strings.ToLower(filter)
	var out []ModelEntry
	for _, m := range r.models {
		if strings.Contains(strings.ToLower(m.Provider), lower) ||
			strings.Contains(strings.ToLower(m.ID), lower) ||
			strings.Contains(strings.ToLower(m.Name), lower) {
			out = append(out, m)
		}
	}
	return out
}

// Cycle returns the next/prev model from the same provider.
// direction > 0 for next, < 0 for previous.
func (r *ModelRegistry) Cycle(currentID string, direction int) *ModelEntry {
	// Find current model's provider
	var prov string
	var currentIdx int = -1
	for i, m := range r.models {
		if strings.EqualFold(m.ID, currentID) {
			prov = m.Provider
			currentIdx = i
			break
		}
	}
	if currentIdx < 0 {
		return nil
	}

	// Collect same-provider models
	var sameProvider []int
	for i, m := range r.models {
		if m.Provider == prov {
			sameProvider = append(sameProvider, i)
		}
	}
	if len(sameProvider) <= 1 {
		return nil
	}

	// Find position in same-provider list
	pos := 0
	for i, idx := range sameProvider {
		if idx == currentIdx {
			pos = i
			break
		}
	}

	// Cycle
	if direction > 0 {
		pos = (pos + 1) % len(sameProvider)
	} else {
		pos = (pos - 1 + len(sameProvider)) % len(sameProvider)
	}
	return &r.models[sameProvider[pos]]
}

// FindByProvider returns all models for a given provider.
func (r *ModelRegistry) FindByProvider(prov string) []ModelEntry {
	var out []ModelEntry
	for _, m := range r.models {
		if strings.EqualFold(m.Provider, prov) {
			out = append(out, m)
		}
	}
	return out
}

// DefaultModel returns the default model for a provider.
func (r *ModelRegistry) DefaultModel(prov string) *ModelEntry {
	defaults := map[string]string{
		"anthropic": "claude-sonnet-4-5",
		"openai":    "gpt-4.1-mini",
		"gemini":    "gemini-2.5-flash",
	}
	if id, ok := defaults[strings.ToLower(prov)]; ok {
		for i := range r.models {
			if r.models[i].ID == id {
				return &r.models[i]
			}
		}
	}
	// Fallback: first model for provider
	for i := range r.models {
		if strings.EqualFold(r.models[i].Provider, prov) {
			return &r.models[i]
		}
	}
	return nil
}

func (r *ModelRegistry) loadBuiltins() {
	r.models = []ModelEntry{
		// Anthropic
		{Provider: "anthropic", ID: "claude-opus-4-6", Name: "Claude Opus 4.6", ContextWindow: 200000, MaxTokens: 32768, Reasoning: true},
		{Provider: "anthropic", ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5", ContextWindow: 200000, MaxTokens: 16384, Reasoning: true},
		{Provider: "anthropic", ID: "claude-sonnet-4-20250514", Name: "Claude Sonnet 4", ContextWindow: 200000, MaxTokens: 16384, Reasoning: true},
		{Provider: "anthropic", ID: "claude-haiku-3-5-20241022", Name: "Claude Haiku 3.5", ContextWindow: 200000, MaxTokens: 8192, Reasoning: false},

		// OpenAI
		{Provider: "openai", ID: "gpt-4.1", Name: "GPT-4.1", ContextWindow: 1047576, MaxTokens: 32768, Reasoning: false},
		{Provider: "openai", ID: "gpt-4.1-mini", Name: "GPT-4.1 Mini", ContextWindow: 1047576, MaxTokens: 16384, Reasoning: false},
		{Provider: "openai", ID: "gpt-4.1-nano", Name: "GPT-4.1 Nano", ContextWindow: 1047576, MaxTokens: 16384, Reasoning: false},
		{Provider: "openai", ID: "o3", Name: "o3", ContextWindow: 200000, MaxTokens: 100000, Reasoning: true},
		{Provider: "openai", ID: "o4-mini", Name: "o4 Mini", ContextWindow: 200000, MaxTokens: 100000, Reasoning: true},

		// Google
		{Provider: "gemini", ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", ContextWindow: 1048576, MaxTokens: 65536, Reasoning: true},
		{Provider: "gemini", ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", ContextWindow: 1048576, MaxTokens: 65536, Reasoning: true},
		{Provider: "gemini", ID: "gemini-2.0-flash", Name: "Gemini 2.0 Flash", ContextWindow: 1048576, MaxTokens: 8192, Reasoning: false},
	}
}

func isValidThinkingLevel(s string) bool {
	switch agentcore.ThinkingLevel(s) {
	case agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow,
		agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh:
		return true
	}
	return false
}

// hasDatedSuffix checks if a model ID ends with a date pattern like -20250514.
func hasDatedSuffix(id string) bool {
	if len(id) < 9 {
		return false
	}
	suffix := id[len(id)-9:]
	if suffix[0] != '-' {
		return false
	}
	for _, c := range suffix[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
