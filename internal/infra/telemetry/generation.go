package telemetry

import (
	"context"
	"encoding/json"
	"time"

	"github.com/voocel/litellm"
	"github.com/voocel/litellm/catalog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// generations adds to litellm/otel's spans what only Langfuse reads: when
// the response began, the usage as Langfuse prices it, and the cost at the
// model's pricing, which codebot shows too. Langfuse takes that cost over
// its own, which it has for fewer models. Each is named chat, the model
// being an attribute: Langfuse keys its views on names, which a change of
// model must not split.
type generations struct {
	litellm.Observer
	pricing *catalog.Pricing
}

func (g generations) Start(ctx context.Context, info litellm.CallInfo) (context.Context, litellm.CallObserver) {
	ctx, inner := g.Observer.Start(ctx, info)
	span := trace.SpanFromContext(ctx)
	span.SetName("chat")
	return ctx, &generation{CallObserver: inner, span: span, pricing: g.pricing}
}

type generation struct {
	litellm.CallObserver
	span    trace.Span
	pricing *catalog.Pricing
	began   time.Time // the first token
}

func (g *generation) OnEvent(ev litellm.Event) {
	switch ev.(type) {
	case litellm.TextDelta, litellm.ReasoningDelta, litellm.ToolUseDelta:
		if g.began.IsZero() {
			g.began = time.Now()
		}
	}
	g.CallObserver.OnEvent(ev)
}

func (g *generation) End(result litellm.CallResult) {
	if !g.began.IsZero() {
		g.span.SetAttributes(attribute.String(attrCompletionStart, g.began.UTC().Format(time.RFC3339Nano)))
	}
	// Without a usage, Langfuse counts the tokens itself.
	if resp := result.Response; resp != nil && resp.Usage != (litellm.Usage{}) {
		g.span.SetAttributes(attribute.String(attrUsage, usageDetails(resp.Usage)))
		if g.pricing != nil {
			if cost, err := g.pricing.Cost(resp.Usage); err == nil {
				g.span.SetAttributes(attribute.String(attrCost, costDetails(cost)))
			}
		}
	}
	g.CallObserver.End(result)
}

// costDetails keys cost by the buckets of usageDetails.
func costDetails(c catalog.Cost) string {
	data, _ := json.Marshal(map[string]float64{
		"input":                c.Input,
		"output":               c.Output,
		"input_cached_tokens":  c.CacheRead,
		"input_cache_creation": c.CacheWrite,
		"total":                c.Total,
	}) // a map of floats
	return string(data)
}

// usageDetails splits u the way Langfuse reads gen_ai usage, into buckets it
// prices apart: the input without cache reads and writes, the output without
// reasoning. The cache writes kept an hour cost more, so they are a bucket
// of their own, which the gen_ai attributes can't express.
func usageDetails(u litellm.Usage) string {
	d := map[string]int{
		"input":  max(u.InputTokens-u.CacheReadTokens-u.CacheWriteTokens, 0),
		"output": max(u.OutputTokens-u.ReasoningTokens, 0),
	}
	for key, n := range map[string]int{
		"input_cached_tokens":     u.CacheReadTokens,
		"input_cache_creation":    u.CacheWriteTokens - u.CacheWrite1hTokens,
		"input_cache_creation_1h": u.CacheWrite1hTokens,
		"output_reasoning_tokens": u.ReasoningTokens,
	} {
		if n > 0 {
			d[key] = n
		}
	}
	data, _ := json.Marshal(d) // a map of ints
	return string(data)
}
