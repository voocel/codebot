package provider

import (
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/codebot/internal/diag"
)

// IsSupportedType reports whether the given provider type is registered in
// litellm (built-in or custom). The check is delegated to agentcore/litellm so
// codebot does not maintain a duplicate whitelist.
func IsSupportedType(name string) bool {
	return llm.IsProviderRegistered(name)
}

// SupportedTypeNames returns all provider names known to litellm, sorted.
func SupportedTypeNames() []string {
	return llm.RegisteredProviders()
}

// CreateModel creates a ChatModel for the given provider, model name, API key, and optional base URL.
func CreateModel(prov, name, apiKey, baseURL string) (agentcore.ChatModel, error) {
	normalizedProvider := strings.ToLower(strings.TrimSpace(prov))
	model, err := llm.NewModel(normalizedProvider, name,
		llm.WithAPIKey(apiKey),
		llm.WithBaseURL(baseURL),
	)
	if err != nil {
		return nil, fmt.Errorf("create model %s/%s: %w: %w",
			normalizedProvider, name, diag.ErrProvider, err)
	}
	applyProviderDefaults(normalizedProvider, name, model)
	return WrapStreamSafe(model), nil
}

func applyProviderDefaults(prov, modelName string, model agentcore.ChatModel) {
	cfgOwner, ok := model.(interface {
		GetConfig() *llm.GenerationConfig
	})
	if !ok {
		return
	}
	cfg := cfgOwner.GetConfig()
	if cfg == nil {
		return
	}

	switch prov {
	case "anthropic":
		limit := anthropicMaxOutputTokens(modelName)
		if cfg.MaxTokens <= 0 || cfg.MaxTokens > limit {
			cfg.MaxTokens = limit
		}
	}
}

func anthropicMaxOutputTokens(modelName string) int {
	if entry, ok := lookupGeneratedModel("anthropic", modelName); ok && entry.MaxTokens > 0 {
		return entry.MaxTokens
	}

	name := strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case strings.Contains(name, "sonnet-4-5"):
		return 64000
	case strings.Contains(name, "haiku-3-5"):
		return 8192
	case strings.Contains(name, "sonnet-3-7"):
		return 16000
	case strings.Contains(name, "opus"):
		return 32000
	default:
		// Conservative fallback so unknown models don't trip a provider 400.
		return 32000
	}
}
