// Package telemetry exports traces over OTLP/HTTP (e.g. to Langfuse):
// generation spans through a litellm observer, run and tool spans through
// Tracer. Every span carries the current session id. When disabled, all of
// it is a no-op.
package telemetry

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync/atomic"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/infra/config"
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

// A nil Tracer does nothing; Setup returns nil when telemetry is disabled.
type Tracer struct {
	tracer    trace.Tracer
	sessionID atomic.Pointer[string]
}

type Run struct {
	span trace.Span
}

func (t *Tracer) StartRun(ctx context.Context, name string) (context.Context, *Run) {
	if t == nil {
		return ctx, nil
	}
	ctx, span := t.tracer.Start(ctx, name)
	span.SetAttributes(t.sessionAttributes()...)
	return ctx, &Run{span: span}
}

func (r *Run) End(err error) {
	if r == nil {
		return
	}
	if err != nil {
		r.span.RecordError(err)
		r.span.SetStatus(codes.Error, err.Error())
	}
	r.span.End()
}

func (t *Tracer) ToolMiddleware() agentcore.ToolMiddleware {
	if t == nil {
		return nil
	}
	return func(ctx context.Context, call agentcore.ToolCall, next agentcore.ToolFunc) (agentcore.Result, error) {
		ctx, span := t.tracer.Start(ctx, "tool "+call.Name)
		span.SetAttributes(append(t.sessionAttributes(),
			attribute.String("tool.name", call.Name),
			attribute.String("tool.call.id", call.ID),
		)...)
		defer span.End()
		res, err := next(ctx, call)
		switch {
		case err != nil:
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		case res.IsError:
			span.SetStatus(codes.Error, res.Text())
		}
		return res, err
	}
}

func (t *Tracer) SetSession(id string) {
	if t == nil {
		return
	}
	t.sessionID.Store(&id)
}

func (t *Tracer) sessionAttributes() []attribute.KeyValue {
	id := t.sessionID.Load()
	if id == nil {
		return nil
	}
	return sessionSpanAttributes(*id)
}

// Setup returns (nil, nil, noop, nil) when telemetry is disabled. The
// returned shutdown flushes pending spans.
func Setup(ctx context.Context, cfg config.TelemetryConfig) (litellm.Observer, *Tracer, func(context.Context) error, error) {
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

	// Name the service so backends show "codebot" instead of
	// "unknown_service". Merging onto the default resource keeps
	// telemetry.sdk.*; using the default schema URL avoids a merge error.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(resource.Default().SchemaURL(), attribute.String("service.name", "codebot")),
	)
	if err != nil {
		res = resource.Default()
	}

	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)

	// The resolver reads the session id on every call, so each generation,
	// run and tool span carries the id of the session open at the time.
	tracer := &Tracer{tracer: tp.Tracer("codebot")}
	resolver := func(context.Context) []attribute.KeyValue {
		return tracer.sessionAttributes()
	}

	observer := litellmotel.New(tp.Tracer("litellm"), litellmotel.WithSpanAttributes(resolver))
	return observer, tracer, tp.Shutdown, nil
}

// sessionSpanAttributes sets langfuse.session.id for Langfuse and session.id
// for other OTLP backends. Tests pin the exact keys, since a typo silently
// breaks session grouping.
func sessionSpanAttributes(id string) []attribute.KeyValue {
	if id == "" {
		return nil
	}
	return []attribute.KeyValue{
		attribute.String("langfuse.session.id", id),
		attribute.String("session.id", id),
	}
}
