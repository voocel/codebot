package provider

import (
	"slices"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/codebot/internal/apperr"
)

var supportedTypeNames = []string{"openai", "anthropic", "gemini", "openrouter", "deepseek"}

type modelFactory func(name, apiKey, baseURL string) (agentcore.ChatModel, error)

var modelFactories = map[string]modelFactory{
	"anthropic": func(name, apiKey, baseURL string) (agentcore.ChatModel, error) {
		if baseURL != "" {
			return llm.NewAnthropicModel(name, apiKey, baseURL)
		}
		return llm.NewAnthropicModel(name, apiKey)
	},
	"gemini": func(name, apiKey, baseURL string) (agentcore.ChatModel, error) {
		if baseURL != "" {
			return llm.NewGeminiModel(name, apiKey, baseURL)
		}
		return llm.NewGeminiModel(name, apiKey)
	},
	"openrouter": func(name, apiKey, baseURL string) (agentcore.ChatModel, error) {
		if baseURL != "" {
			return llm.NewOpenRouterModel(name, apiKey, baseURL)
		}
		return llm.NewOpenRouterModel(name, apiKey)
	},
	"openai": func(name, apiKey, baseURL string) (agentcore.ChatModel, error) {
		if baseURL != "" {
			return llm.NewOpenAIModel(name, apiKey, baseURL)
		}
		return llm.NewOpenAIModel(name, apiKey)
	},
	"deepseek": func(name, apiKey, baseURL string) (agentcore.ChatModel, error) {
		if baseURL != "" {
			return llm.NewDeepSeekModel(name, apiKey, baseURL)
		}
		return llm.NewDeepSeekModel(name, apiKey)
	},
}

// SupportedTypeNames returns the supported LiteLLM provider type names in stable order.
func SupportedTypeNames() []string {
	return slices.Clone(supportedTypeNames)
}

// IsSupportedType reports whether the given provider type is supported.
func IsSupportedType(name string) bool {
	_, ok := modelFactories[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// CreateModel creates a ChatModel for the given provider, model name, API key, and optional base URL.
func CreateModel(prov, name, apiKey, baseURL string) (agentcore.ChatModel, error) {
	normalizedProvider := strings.ToLower(strings.TrimSpace(prov))
	model, err := newProviderModel(normalizedProvider, name, apiKey, baseURL)
	if err != nil {
		return nil, err
	}
	applyProviderDefaults(normalizedProvider, name, model)
	return WrapStreamSafe(model), nil
}

func newProviderModel(prov, name, apiKey, baseURL string) (agentcore.ChatModel, error) {
	factory, ok := modelFactories[prov]
	if !ok {
		return nil, apperr.NewKindf(apperr.KindProvider, "unsupported provider type %q", prov)
	}
	return factory(name, apiKey, baseURL)
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
