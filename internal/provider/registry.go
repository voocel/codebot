package provider

//go:generate go run gen_models.go

import (
	"fmt"
	"strings"
	"sync"

	"github.com/voocel/agentcore"
)

// ModelEntry describes a known LLM model.
type ModelEntry struct {
	Provider             string  `json:"provider"`
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	ContextWindow        int     `json:"context_window"`
	MaxTokens            int     `json:"max_tokens"`
	Reasoning            bool    `json:"reasoning"`
	InputCostPer1M       float64 `json:"input_cost_per_1m"`
	OutputCostPer1M      float64 `json:"output_cost_per_1m"`
	CacheReadCostPer1M   float64 `json:"cache_read_cost_per_1m"`
	CacheWriteCostPer1M  float64 `json:"cache_write_cost_per_1m"`
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
		if isValidThinkingLevel(suffix) {
			thinkingLevel = agentcore.ThinkingLevel(suffix)
			pattern = pattern[:idx]
		}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	// Try provider/model format
	if idx := strings.Index(pattern, "/"); idx > 0 {
		prov := pattern[:idx]
		modelID := pattern[idx+1:]
		for i := range r.models {
			if strings.EqualFold(r.models[i].Provider, prov) &&
				strings.EqualFold(r.models[i].ID, modelID) {
				entry := r.models[i]
				return &entry, thinkingLevel, nil
			}
		}
	}

	// Try exact ID match
	for i := range r.models {
		if strings.EqualFold(r.models[i].ID, pattern) {
			entry := r.models[i]
			return &entry, thinkingLevel, nil
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
	r.mu.RLock()
	defer r.mu.RUnlock()

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
	entry := r.models[sameProvider[pos]]
	return &entry
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
func (r *ModelRegistry) CostRates(modelID string) (inputPer1M, outputPer1M, cacheReadPer1M, cacheWritePer1M float64) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, m := range r.models {
		if strings.EqualFold(m.ID, modelID) {
			return m.InputCostPer1M, m.OutputCostPer1M, m.CacheReadCostPer1M, m.CacheWriteCostPer1M
		}
	}
	return 0, 0, 0, 0
}

// DefaultModel returns the first model for a provider.
func (r *ModelRegistry) DefaultModel(prov string) *ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for i := range r.models {
		if strings.EqualFold(r.models[i].Provider, prov) {
			entry := r.models[i]
			return &entry
		}
	}
	return nil
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

// ThinkingLevelOrder defines the ordered progression of thinking levels.
var ThinkingLevelOrder = []string{"off", "minimal", "low", "medium", "high", "xhigh"}

// AvailableThinkingLevels returns valid thinking levels for a model.
// Non-reasoning models only support "off"; reasoning models support all levels.
func (r *ModelRegistry) AvailableThinkingLevels(modelID string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, m := range r.models {
		if strings.EqualFold(m.ID, modelID) {
			if m.Reasoning {
				return ThinkingLevelOrder
			}
			return []string{"off"}
		}
	}
	// Unknown model — assume all levels are valid.
	return ThinkingLevelOrder
}

// ClampThinkingLevel adjusts level to the nearest valid level from available.
// It searches backward then forward in ThinkingLevelOrder for the closest match.
func ClampThinkingLevel(level string, available []string) string {
	if len(available) == 0 {
		return "off"
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
