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

// IsSupportedType defers to litellm rather than keeping its own list. Type
// "gateway" reaches a litellm gateway that holds the keys and makes the
// calls, so codebot can run where keys must not live, such as a sandbox.
func IsSupportedType(name string) bool {
	return slices.Contains(llmprovider.Names(), name)
}

type ModelSpec struct {
	// Type is the litellm provider that Provider speaks; they differ for
	// custom providers.
	Provider string
	Type     string
	Model    string
	Conn     llmprovider.Config
}

// fallbackMaxTokens is the output cap for unlisted models on providers that
// require one; every Claude 4 or later model accepts it.
const fallbackMaxTokens = 32000

// NewModelFactory names each provider as configured, so replay state goes
// back only to the endpoint that issued it.
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
		if caps, _ := client.Capabilities(); caps.MaxTokensRequired {
			model.Request.MaxTokens = new(cmp.Or(facts.MaxOutputTokens, fallbackMaxTokens))
		}
		return model, nil
	}
}

// WithCacheKey sets prompt_cache_key only on providers that accept it;
// others reject options they do not know.
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
