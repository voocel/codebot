package provider

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	llmprovider "github.com/voocel/litellm/provider"
)

// IsSupportedType reports whether codebot builds a provider of the given
// type: one litellm builds, so codebot keeps no list of its own. Type
// "gateway" reaches a litellm gateway, which holds the keys, makes the model
// calls and bills them, so codebot can run where keys must not, say in a
// sandbox.
func IsSupportedType(name string) bool {
	return slices.Contains(llmprovider.Names(), name)
}

// ModelSpec names a model and how to reach the provider serving it.
type ModelSpec struct {
	// Provider is the configured provider name; Type is the litellm provider
	// it speaks, which differs for custom providers.
	Provider string
	Type     string
	Model    string
	Conn     llmprovider.Config
}

// fallbackMaxTokens caps models missing from the model list on providers that
// require a cap; every Claude 4 or later model accepts it.
const fallbackMaxTokens = 32000

// NewModelFactory returns a factory that builds models with the output caps
// and prices in models, forwarding clientOpts (e.g. litellm.WithObservers for
// telemetry) to every client. A provider is named as configured, so replay
// state reaches only the endpoint that issued it.
func NewModelFactory(models *Models, clientOpts ...litellm.ClientOption) func(ModelSpec) (agentcore.Model, error) {
	return func(spec ModelSpec) (agentcore.Model, error) {
		conn := spec.Conn
		conn.Name = spec.Provider
		p, err := llmprovider.New(spec.Type, conn)
		if err != nil {
			return agentcore.Model{}, fmt.Errorf("create model %s/%s: %w", spec.Provider, spec.Model, err)
		}
		client, err := litellm.New(p, clientOpts...)
		if err != nil {
			return agentcore.Model{}, fmt.Errorf("create model %s/%s: %w", spec.Provider, spec.Model, err)
		}
		facts, _ := models.Lookup(spec)
		model := agentcore.Model{Client: client, Request: litellm.Request{Model: spec.Model}, Pricing: facts.Pricing}
		// Providers that require an output cap, such as Anthropic, get the model's.
		if caps, _ := client.Capabilities(); caps.MaxTokensRequired {
			model.Request.MaxTokens = new(cmp.Or(facts.MaxOutputTokens, fallbackMaxTokens))
		}
		return model, nil
	}
}

// WithCacheKey returns model with its requests routing the prompt cache by
// key, as OpenAI's prompt_cache_key does, where its provider takes a key:
// the others reject options they do not list.
func WithCacheKey(model agentcore.Model, key string) agentcore.Model {
	const option = "prompt_cache_key"
	if caps, _ := model.Client.Capabilities(); !slices.Contains(caps.ProviderOptions, option) {
		return model
	}
	opts := maps.Clone(model.Request.ProviderOptions)
	if opts == nil {
		opts = litellm.ProviderOptions{}
	}
	if err := opts.Set(option, key); err != nil {
		panic(err) // a string always encodes
	}
	model.Request.ProviderOptions = opts
	return model
}
