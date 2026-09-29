package provider

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/codebot/internal/diag"
	"github.com/voocel/litellm"
	llmprovider "github.com/voocel/litellm/provider"
)

// IsSupportedType reports whether litellm builds a provider of the given type,
// so codebot does not maintain a duplicate whitelist.
func IsSupportedType(name string) bool {
	return slices.Contains(llmprovider.Names(), name)
}

// SupportedTypeNames returns the provider types litellm builds, sorted.
func SupportedTypeNames() []string {
	return llmprovider.Names()
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
// telemetry) to every client. Its type matches agent.ModelFactory without
// importing that package, avoiding an import cycle.
func NewModelFactory(models *Models, clientOpts ...litellm.ClientOption) func(ModelSpec) (agentcore.ChatModel, error) {
	return func(spec ModelSpec) (agentcore.ChatModel, error) {
		facts, _ := models.Lookup(spec)
		opts := []llm.ModelOption{llm.WithClientOptions(clientOpts...)}
		if facts.Pricing != nil {
			opts = append(opts, llm.WithPricing(*facts.Pricing))
		}
		// Providers that require an output cap, such as Anthropic, get the model's.
		opts = append(opts, llm.WithMaxTokensIfRequired(cmp.Or(facts.MaxOutputTokens, fallbackMaxTokens)))
		model, err := llm.NewModel(spec.Type, spec.Model, spec.Conn, opts...)
		if err != nil {
			return nil, fmt.Errorf("create model %s/%s: %w: %w", spec.Provider, spec.Model, diag.ErrProvider, err)
		}
		return WrapStreamSafe(model), nil
	}
}
