// Package telemetry wires codebot's observability. It builds an OTLP/HTTP
// trace exporter for the configured backend (e.g. Langfuse), returns a litellm
// hook for generation spans, and exposes a small Tracer for agent-run/tool
// spans. Every span is tagged with the current session id so the backend can
// group a session's work. When disabled it is a no-op, so the rest of the app
// stays unaware of OpenTelemetry.
package telemetry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/litellm"
	litellmotel "github.com/voocel/litellm/otel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Tracer creates codebot-level spans that share the same tracer provider as the
// litellm hook. It is nil when telemetry is disabled.
type Tracer struct {
	tracer          trace.Tracer
	sessionProvider atomic.Pointer[func() string]
}

// Run holds an open agent-run span. End must be called when the agent emits
// EventAgentEnd; Prompt/Continue return before the background run finishes.
type Run struct {
	span trace.Span
}

// StartRun starts an agent-run span and returns the child context for Agent.Prompt/Continue.
func (t *Tracer) StartRun(ctx context.Context, name string) (context.Context, *Run) {
	if t == nil {
		return ctx, nil
	}
	ctx, span := t.tracer.Start(ctx, name)
	span.SetAttributes(t.sessionAttributes()...)
	return ctx, &Run{span: span}
}

// End finishes the agent-run span and records err when present.
func (r *Run) End(err error) {
	if r == nil || r.span == nil {
		return
	}
	if err != nil {
		r.span.RecordError(err)
		r.span.SetStatus(codes.Error, err.Error())
	}
	r.span.End()
	r.span = nil
}

// ToolMiddleware emits one child span per tool execution.
func (t *Tracer) ToolMiddleware() agentcore.ToolMiddleware {
	if t == nil {
		return nil
	}
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolExecuteFunc) (json.RawMessage, error) {
		name := call.Name
		if name == "" {
			name = "tool"
		}
		ctx, span := t.tracer.Start(ctx, "tool "+name)
		span.SetAttributes(append(t.sessionAttributes(),
			attribute.String("tool.name", call.Name),
			attribute.String("tool.call.id", call.ID),
		)...)
		defer span.End()
		out, err := next(ctx, call.Args)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return out, err
	}
}

// BindSession registers the provider that yields the current session id.
func (t *Tracer) BindSession(provider func() string) {
	if t == nil {
		return
	}
	t.sessionProvider.Store(&provider)
}

func (t *Tracer) sessionAttributes() []attribute.KeyValue {
	if t == nil {
		return nil
	}
	p := t.sessionProvider.Load()
	if p == nil {
		return nil
	}
	return sessionSpanAttributes((*p)())
}

// Setup builds the trace pipeline for cfg and returns a litellm hook, a
// codebot tracer, and a shutdown func that flushes pending spans. When telemetry
// is disabled it returns (nil, nil, noop, nil).
func Setup(ctx context.Context, cfg config.TelemetryConfig) (litellm.Hook, *Tracer, func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if !cfg.Enabled || cfg.Endpoint == "" {
		return nil, nil, noop, nil
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
		return nil, nil, noop, fmt.Errorf("telemetry: otlp exporter: %w", err)
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
	// resolver reads it live on every call, so each generation/run/tool span
	// carries the current session id and a mid-run SwitchSession follows.
	tracer := &Tracer{tracer: tp.Tracer("codebot")}
	resolver := func(context.Context) []attribute.KeyValue {
		return tracer.sessionAttributes()
	}

	hook := litellmotel.New(tp.Tracer("litellm"), litellmotel.WithSpanAttributes(resolver))
	return hook, tracer, tp.Shutdown, nil
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
