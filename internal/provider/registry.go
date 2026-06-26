package provider

//go:generate go run gen_models.go

import (
	"fmt"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

// ModelEntry describes a known LLM model.
type ModelEntry struct {
	Provider            string  `json:"provider"`
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	ContextWindow       int     `json:"context_window"`
	MaxTokens           int     `json:"max_tokens"`
	Reasoning           bool    `json:"reasoning"`
	InputCostPer1M      float64 `json:"input_cost_per_1m"`
	OutputCostPer1M     float64 `json:"output_cost_per_1m"`
	CacheReadCostPer1M  float64 `json:"cache_read_cost_per_1m"`
	CacheWriteCostPer1M float64 `json:"cache_write_cost_per_1m"`
}

// ModelRegistry holds known models and provides resolution/cycling.
type ModelRegistry struct {
	mu     sync.RWMutex
	models []ModelEntry
}

// NewModelRegistry creates a registry loaded from generated model data.
func NewModelRegistry() *ModelRegistry {
	r := &ModelRegistry{}
	r.models = append(r.models, generatedModels...)
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
		if IsValidThinkingLevel(suffix) {
			thinkingLevel = agentcore.NormalizeThinkingLevel(agentcore.ThinkingLevel(suffix))
			pattern = pattern[:idx]
		}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	// Try provider/model format
	if idx := strings.Index(pattern, "/"); idx > 0 {
		prov := pattern[:idx]
		modelID := pattern[idx+1:]
		if entry, ok := lookupModelEntry(r.models, prov, modelID); ok {
			return &entry, thinkingLevel, nil
		}
	}

	// Try exact ID match
	if entry, ok := lookupModelEntry(r.models, "", pattern); ok {
		return &entry, thinkingLevel, nil
	}

	// Try partial match (ID or Name contains pattern)
	lower := strings.ToLower(pattern)
	normalizedPattern := normalizeModelLookupID(pattern)
	var candidates []int
	for i := range r.models {
		if strings.Contains(normalizeModelLookupID(r.models[i].ID), normalizedPattern) ||
			strings.Contains(strings.ToLower(r.models[i].ID), lower) ||
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
	entry := r.models[best]
	return &entry, thinkingLevel, nil
}

// List returns models matching the filter (empty filter = all).
func (r *ModelRegistry) List(filter string) []ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if filter == "" {
		return append([]ModelEntry{}, r.models...)
	}
	lower := strings.ToLower(filter)
	normalizedFilter := normalizeModelLookupID(filter)
	var out []ModelEntry
	for _, m := range r.models {
		if strings.Contains(strings.ToLower(m.Provider), lower) ||
			strings.Contains(normalizeModelLookupID(m.ID), normalizedFilter) ||
			strings.Contains(strings.ToLower(m.ID), lower) ||
			strings.Contains(strings.ToLower(m.Name), lower) {
			out = append(out, m)
		}
	}
	return out
}

// FindByProvider returns all models for a given provider.
func (r *ModelRegistry) FindByProvider(prov string) []ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []ModelEntry
	for _, m := range r.models {
		if strings.EqualFold(m.Provider, prov) {
			out = append(out, m)
		}
	}
	return out
}

// CostRates returns the per-1M-token cost rates for a model.
// Uses Resolve for matching, so partial names like "sonnet" work.
func (r *ModelRegistry) CostRates(modelID string) (inputPer1M, outputPer1M, cacheReadPer1M, cacheWritePer1M float64) {
	entry, _, err := r.Resolve(modelID)
	if err != nil {
		return 0, 0, 0, 0
	}
	return entry.InputCostPer1M, entry.OutputCostPer1M, entry.CacheReadCostPer1M, entry.CacheWriteCostPer1M
}

// MergeModels updates existing models and adds new ones from fetched data.
// Matching by Provider+ID (case-insensitive). Updates cost/context/name fields.
func (r *ModelRegistry) MergeModels(fetched []ModelEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	idx := make(map[string]int, len(r.models))
	for i, m := range r.models {
		idx[strings.ToLower(m.Provider+"/"+m.ID)] = i
	}
	for _, f := range fetched {
		key := strings.ToLower(f.Provider + "/" + f.ID)
		if i, ok := idx[key]; ok {
			if f.InputCostPer1M > 0 || f.OutputCostPer1M > 0 {
				r.models[i].InputCostPer1M = f.InputCostPer1M
				r.models[i].OutputCostPer1M = f.OutputCostPer1M
				r.models[i].CacheReadCostPer1M = f.CacheReadCostPer1M
				r.models[i].CacheWriteCostPer1M = f.CacheWriteCostPer1M
			}
			if f.ContextWindow > 0 {
				r.models[i].ContextWindow = f.ContextWindow
			}
			if f.MaxTokens > 0 {
				r.models[i].MaxTokens = f.MaxTokens
			}
			if f.Name != "" {
				r.models[i].Name = f.Name
			}
		} else {
			r.models = append(r.models, f)
			idx[key] = len(r.models) - 1
		}
	}
}

// IsValidThinkingLevel reports whether s is a recognized thinking level.
func IsValidThinkingLevel(s string) bool {
	switch agentcore.NormalizeThinkingLevel(agentcore.ThinkingLevel(s)) {
	case agentcore.ThinkingAuto, agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow,
		agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh,
		agentcore.ThinkingMax:
		return true
	}
	return false
}

// hasDatedSuffix checks if a model ID ends with a date pattern like -20250514.
func hasDatedSuffix(id string) bool {
	if len(id) < 9 {
		return false
	}
	return isDatedModelSuffix(id[len(id)-9:])
}

// ThinkingLevelOrder defines the ordered progression of thinking levels.
var ThinkingLevelOrder = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

func ThinkingLevelsForModel(model agentcore.ChatModel) []string {
	if model == nil {
		return nil
	}
	return thinkingLevelsFromPolicy(llm.ThinkingPolicyFor(model))
}

func ResolveThinkingLevel(model agentcore.ChatModel, level string) (string, bool) {
	if model == nil {
		return string(agentcore.NormalizeThinkingLevel(agentcore.ThinkingLevel(level))), true
	}
	resolved, ok := llm.ThinkingPolicyFor(model).Resolve(agentcore.ThinkingLevel(level))
	return string(resolved), ok
}

// AvailableThinkingLevels returns valid thinking levels for a model.
// Non-reasoning models only support "off"; reasoning models support all levels.
func (r *ModelRegistry) AvailableThinkingLevels(modelID string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if entry, ok := lookupModelEntry(r.models, "", modelID); ok {
		if entry.Reasoning {
			return ThinkingLevelOrder
		}
		return []string{"off"}
	}
	// Unknown model — assume all levels are valid.
	return ThinkingLevelOrder
}

func thinkingLevelsFromPolicy(policy llm.ThinkingPolicy) []string {
	if len(policy.Available) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(policy.Available))
	for _, level := range policy.Available {
		out = append(out, string(level))
	}
	return out
}

// ClampThinkingLevel adjusts level to the nearest valid level from available.
// It searches backward then forward in ThinkingLevelOrder for the closest match.
func ClampThinkingLevel(level string, available []string) string {
	level = string(agentcore.NormalizeThinkingLevel(agentcore.ThinkingLevel(level)))
	if len(available) == 0 {
		return ""
	}
	avSet := make(map[string]bool, len(available))
	for _, a := range available {
		avSet[a] = true
	}
	if avSet[level] {
		return level
	}

	// Find position of requested level in the ordered list.
	pos := -1
	for i, l := range ThinkingLevelOrder {
		if l == level {
			pos = i
			break
		}
	}
	if pos < 0 {
		return available[0]
	}

	// Search backward then forward for nearest available level.
	for dist := 1; dist < len(ThinkingLevelOrder); dist++ {
		if lo := pos - dist; lo >= 0 && avSet[ThinkingLevelOrder[lo]] {
			return ThinkingLevelOrder[lo]
		}
		if hi := pos + dist; hi < len(ThinkingLevelOrder) && avSet[ThinkingLevelOrder[hi]] {
			return ThinkingLevelOrder[hi]
		}
	}
	return available[0]
}
