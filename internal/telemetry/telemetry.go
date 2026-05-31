// Package telemetry wires codebot's LLM observability. It builds an OTLP/HTTP
// trace exporter for the configured backend (e.g. Langfuse) and returns a
// litellm hook that emits one generation span per LLM call, plus a shutdown
// func that flushes pending spans. When disabled it is a no-op, so the rest of
// the app stays unaware of OpenTelemetry.
package telemetry

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/litellm"
	litellmotel "github.com/voocel/litellm/otel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup builds the trace pipeline for cfg and returns a litellm hook to
// register on every model plus a shutdown func that flushes pending spans.
// When telemetry is disabled it returns (nil, noop, nil); callers register the
// hook only when it is non-nil.
func Setup(ctx context.Context, cfg config.TelemetryConfig) (litellm.Hook, func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if !cfg.Enabled || cfg.Endpoint == "" {
		return nil, noop, nil
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
		return nil, noop, fmt.Errorf("telemetry: otlp exporter: %w", err)
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

	return litellmotel.New(tp.Tracer("litellm")), tp.Shutdown, nil
}
