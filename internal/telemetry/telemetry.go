// Package telemetry wires codebot's LLM observability. It builds an OTLP/HTTP
// trace exporter for the configured backend (e.g. Langfuse) and returns a
// litellm hook that emits one generation span per LLM call — each tagged with
// the current session id so the backend groups a run's calls — plus a
// BindSession to supply that id once the session exists and a shutdown func
// that flushes pending spans. When disabled it is a no-op, so the rest of the
// app stays unaware of OpenTelemetry.
package telemetry

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync/atomic"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/litellm"
	litellmotel "github.com/voocel/litellm/otel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// BindSession registers the provider that yields the current session id, used
// to tag every generation span so the backend (e.g. Langfuse) can group a
// session's calls. The hook is built at boot, before the session exists, so the
// binding happens later; the provider is read live on each call, so a mid-run
// session switch is reflected without re-binding.
type BindSession func(provider func() string)

// Setup builds the trace pipeline for cfg and returns: a litellm hook to
// register on every model, a BindSession to wire the session-id provider once
// the session is built, and a shutdown func that flushes pending spans. When
// telemetry is disabled it returns (nil, no-op bind, no-op shutdown, nil);
// callers register the hook only when it is non-nil.
func Setup(ctx context.Context, cfg config.TelemetryConfig) (litellm.Hook, BindSession, func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	noBind := func(func() string) {}
	if !cfg.Enabled || cfg.Endpoint == "" {
		return nil, noBind, noop, nil
	}

	opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(cfg.Endpoint)}
	if cfg.PublicKey != "" || cfg.SecretKey != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(cfg.PublicKey + ":" + cfg.SecretKey))
		opts = append(opts, otlptracehttp.WithHeaders(map[string]string{
			"Authorization": "Basic " + auth,
		}))
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, noBind, noop, fmt.Errorf("telemetry: otlp exporter: %w", err)
	}

	// Name the service so backends show "codebot" instead of the OTel default
	// "unknown_service". Merge onto the default resource to keep telemetry.sdk.*;
	// matching the default schema URL keeps the merge error-free.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(resource.Default().SchemaURL(), attribute.String("service.name", "codebot")),
	)
	if err != nil {
		res = resource.Default()
	}

	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)

	// The session-id provider is bound after the session is constructed. The
	// resolver reads it live on every call, so each generation span carries the
	// current session id and a mid-run switch needs no re-binding.
	var sessionProvider atomic.Pointer[func() string]
	bind := func(provider func() string) { sessionProvider.Store(&provider) }
	resolver := func(context.Context) []attribute.KeyValue {
		p := sessionProvider.Load()
		if p == nil {
			return nil
		}
		return sessionSpanAttributes((*p)())
	}

	hook := litellmotel.New(tp.Tracer("litellm"), litellmotel.WithSpanAttributes(resolver))
	return hook, bind, tp.Shutdown, nil
}

// sessionSpanAttributes maps a session id to the span attributes used to group
// a run's generations on the backend. langfuse.session.id is Langfuse's primary
// session key; session.id is the generic fallback other OTLP backends read. An
// empty id yields nil (no tagging) — exact key strings matter, a typo silently
// breaks session grouping, so they are asserted in tests.
func sessionSpanAttributes(id string) []attribute.KeyValue {
	if id == "" {
		return nil
	}
	return []attribute.KeyValue{
		attribute.String("langfuse.session.id", id),
		attribute.String("session.id", id),
	}
}
