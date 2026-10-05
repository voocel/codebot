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

// defaultWindow applies to models the model list does not know.
const defaultWindow = 128_000

// modelChoice is the model a conversation runs on and what follows from it.
type modelChoice struct {
	provider string
	name     string
	// model is the model without the reasoning effort, which only the
	// conversation's own calls use: sub-agents and prompt hooks take the
	// provider default.
	model     agentcore.Model
	effort    string
	window    int // effective context window
	compactAt int // estimated request size that triggers compaction
	small     string
}

// chooseModel builds the model name served by the provider configured as
// prov, with a reasoning effort it supports.
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
	if !slices.Contains(provider.ThinkingLevels(model.Client, facts.Reasoning), effort) {
		return modelChoice{}, fmt.Errorf("model %s/%s: unsupported reasoning_effort %q", prov, name, effort)
	}

	window, maxOutput := defaultWindow, 0
	if ok && facts.MaxInputTokens > 0 {
		window, maxOutput = facts.MaxInputTokens, facts.MaxOutputTokens
	}
	// The user cap only ever lowers the window: above the model's own the
	// provider rejects the request.
	if cap := a.settings.CompactWindow; cap > 0 && cap < window {
		window = cap
	}

	return modelChoice{
		provider:  prov,
		name:      name,
		model:     model,
		effort:    effort,
		window:    window,
		compactAt: window - compactReserve(window, maxOutput, a.settings.CompactRatio),
		small:     a.smallModel(prov, name),
	}, nil
}

// compactReserve is the room left in window for the model's reply: what the
// compaction ratio leaves, if one is set, else the model's output ceiling,
// at most 20k tokens and half the window. A model of unknown ceiling gets
// 13% of the window, between 4k and 16k tokens.
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

// smallModel is the model the explore sub-agent runs on: the provider's
// small_model, else the model itself.
func (a *App) smallModel(prov, name string) string {
	return cmp.Or(a.settings.Providers[prov].SmallModel, name)
}

// resolveModelName builds a model named in an agent definition: served by
// prov when it lists the model, else by the first provider (by name) that
// does, else by prov.
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

// ModelFacts returns what the model list knows about a model.
func (a *App) ModelFacts(prov, name string) (catalog.Model, bool) {
	spec, err := config.ModelSpec(a.settings.Providers, prov, name)
	if err != nil {
		return catalog.Model{}, false
	}
	return a.models.Lookup(spec)
}

// ThinkingLevels lists the reasoning efforts a model accepts; "" is the
// provider default.
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
