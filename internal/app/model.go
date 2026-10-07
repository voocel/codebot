package app

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm/catalog"

	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/infra/provider"
)

// defaultWindow is used for models missing from the model list.
const defaultWindow = 128_000

type modelChoice struct {
	provider string
	name     string
	// model has no reasoning effort set: only the conversation's own calls
	// use effort, while sub-agents and prompt hooks use the provider default.
	model     agentcore.Model
	effort    string
	reasoning bool // the model accepts a reasoning effort
	window    int  // effective context window
	compactAt int  // estimated request size that triggers compaction
	small     string
}

func (a *App) chooseModel(prov, name, effort string) (modelChoice, error) {
	spec, err := config.ModelSpec(a.settings.Providers, prov, name)
	if err != nil {
		return modelChoice{}, err
	}
	model, err := a.newModel(spec)
	if err != nil {
		return modelChoice{}, fmt.Errorf("create model failed: %w", err)
	}
	facts, ok := a.models.Lookup(spec)
	levels := provider.ThinkingLevels(model.Client, facts.Reasoning)
	if !slices.Contains(levels, effort) {
		return modelChoice{}, fmt.Errorf("model %s/%s: unsupported reasoning_effort %q", prov, name, effort)
	}

	window, maxOutput := defaultWindow, 0
	if ok && facts.MaxInputTokens > 0 {
		window, maxOutput = facts.MaxInputTokens, facts.MaxOutputTokens
	}
	// The user's cap can only lower the window; the provider rejects requests
	// above the model's own.
	if cap := a.settings.CompactWindow; cap > 0 && cap < window {
		window = cap
	}

	return modelChoice{
		provider:  prov,
		name:      name,
		model:     model,
		effort:    effort,
		reasoning: len(levels) > 1,
		window:    window,
		compactAt: window - compactReserve(window, maxOutput, a.settings.CompactRatio),
		small:     a.smallModel(prov, name),
	}, nil
}

// compactReserve is the room left in the window for the model's reply.
func compactReserve(window, maxOutput int, ratio float64) int {
	switch {
	case ratio > 0:
		return window - int(float64(window)*ratio)
	case maxOutput > 0:
		return min(20_000, maxOutput, window/2)
	default:
		return min(16_384, max(4_096, window*13/100))
	}
}

func (a *App) smallModel(prov, name string) string {
	return cmp.Or(a.settings.Providers[prov].SmallModel, name)
}

// resolveModelName resolves a model named in an agent definition. It prefers
// prov, then the first provider (by name) that lists the model.
func (a *App) resolveModelName(prov, name string) (agentcore.Model, error) {
	lists := func(p string) bool {
		return slices.ContainsFunc(a.settings.Providers[p].Models, func(m string) bool { return strings.EqualFold(m, name) })
	}
	if !lists(prov) {
		for _, p := range slices.Sorted(maps.Keys(a.settings.Providers)) {
			if lists(p) {
				prov = p
				break
			}
		}
	}
	spec, err := config.ModelSpec(a.settings.Providers, prov, name)
	if err != nil {
		return agentcore.Model{}, err
	}
	return a.newModel(spec)
}

func (a *App) ModelFacts(prov, name string) (catalog.Model, bool) {
	spec, err := config.ModelSpec(a.settings.Providers, prov, name)
	if err != nil {
		return catalog.Model{}, false
	}
	return a.models.Lookup(spec)
}

// ThinkingLevels includes "" for the provider default.
func (a *App) ThinkingLevels(prov, name string) []string {
	spec, err := config.ModelSpec(a.settings.Providers, prov, name)
	if err != nil {
		return []string{""}
	}
	model, err := a.newModel(spec)
	if err != nil {
		return []string{""}
	}
	facts, _ := a.models.Lookup(spec)
	return provider.ThinkingLevels(model.Client, facts.Reasoning)
}
