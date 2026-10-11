package telemetry

import (
	"context"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// secrets are credentials recognizable by their form. The model's provider
// sees one the agent reads; the traces would hand it to Langfuse as well.
var secrets = regexp.MustCompile(strings.Join([]string{
	`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`,
	`\b(?:sk|rk)-[A-Za-z0-9_-]{20,}`,             // OpenAI, Anthropic, Langfuse and the like
	`\b[sr]k_(?:live|test)_[A-Za-z0-9]{16,}`,     // Stripe
	`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`,              // AWS access key IDs
	`\bgh[pousr]_[A-Za-z0-9]{36,}`,               // GitHub
	`\bgithub_pat_[A-Za-z0-9_]{22,}`,             // GitHub fine-grained
	`\bAIza[0-9A-Za-z_-]{35}`,                    // Google
	`\bxox[abprs]-[A-Za-z0-9-]{10,}`,             // Slack
	`\beyJ[\w-]{10,}\.eyJ[\w-]{10,}\.[\w-]{10,}`, // JWTs
}, "|"))

const redacted = "[redacted]"

// redacting cuts the secrets out of the spans it sends.
type redacting struct{ sdktrace.SpanExporter }

func (r redacting) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		out[i] = redact(s)
	}
	return r.SpanExporter.ExportSpans(ctx, out)
}

func redact(s sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	attrs, changed := redactAttrs(s.Attributes())
	events := s.Events()
	var eventsChanged bool
	for i, e := range events {
		if a, ok := redactAttrs(e.Attributes); ok {
			if !eventsChanged {
				events, eventsChanged = append([]sdktrace.Event(nil), events...), true
			}
			events[i].Attributes = a
		}
	}
	status := s.Status()
	if d := secrets.ReplaceAllString(status.Description, redacted); d != status.Description {
		status.Description, changed = d, true
	}
	if !changed && !eventsChanged {
		return s
	}
	return redactedSpan{s, attrs, events, status}
}

// redactAttrs returns attrs with the secrets cut, and whether there were any.
func redactAttrs(attrs []attribute.KeyValue) ([]attribute.KeyValue, bool) {
	var out []attribute.KeyValue
	for i, kv := range attrs {
		if kv.Value.Type() != attribute.STRING {
			continue
		}
		v := kv.Value.AsString()
		if cut := secrets.ReplaceAllString(v, redacted); cut != v {
			if out == nil {
				out = append([]attribute.KeyValue(nil), attrs...)
			}
			out[i] = attribute.String(string(kv.Key), cut)
		}
	}
	if out == nil {
		return attrs, false
	}
	return out, true
}

type redactedSpan struct {
	sdktrace.ReadOnlySpan
	attrs  []attribute.KeyValue
	events []sdktrace.Event
	status sdktrace.Status
}

func (s redactedSpan) Attributes() []attribute.KeyValue { return s.attrs }
func (s redactedSpan) Events() []sdktrace.Event         { return s.events }
func (s redactedSpan) Status() sdktrace.Status          { return s.status }
