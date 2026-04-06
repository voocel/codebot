package provider

import (
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/litellm"
)

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
	switch prov {
	case "anthropic":
		if baseURL != "" {
			return llm.NewAnthropicModel(name, apiKey, baseURL)
		}
		return llm.NewAnthropicModel(name, apiKey)
	case "gemini":
		if baseURL != "" {
			return llm.NewGeminiModel(name, apiKey, baseURL)
		}
		return llm.NewGeminiModel(name, apiKey)
	case "openrouter":
		if baseURL != "" {
			return llm.NewOpenRouterModel(name, apiKey, baseURL)
		}
		return llm.NewOpenRouterModel(name, apiKey)
	case "openai":
		if baseURL != "" {
			return llm.NewOpenAIModel(name, apiKey, baseURL)
		}
		return llm.NewOpenAIModel(name, apiKey)
	case "glm":
		return newGLMModel(name, apiKey, baseURL)
	default:
		if baseURL != "" {
			return llm.NewOpenAIModel(name, apiKey, baseURL)
		}
		return llm.NewOpenAIModel(name, apiKey)
	}
}

func newGLMModel(model, apiKey, baseURL string) (agentcore.ChatModel, error) {
	cfg := litellm.ProviderConfig{APIKey: apiKey}
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	client, err := litellm.NewWithProvider("glm", cfg)
	if err != nil {
		return nil, fmt.Errorf("glm: %w", err)
	}
	return llm.NewLiteLLMAdapter(model, client), nil
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
		// Conservative fallback to avoid provider 400s when model-specific limits are unknown.
		return 32000
	}
}
