// Package telemetry traces codebot's work to Langfuse over OpenTelemetry.
// Each user turn is a trace: an agent observation holding the turn's input
// and answer, with the model calls, tool calls, approvals and sub-agents
// beneath it, each with what went in and came out. Work outside a turn, such
// as compacting or a side question, is a trace of its own, named for it.
// Every observation carries the conversation's ID as its Langfuse session.
//
// A nil Tracer records nothing.
package telemetry

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os/user"
	"regexp"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/catalog"
	litellmotel "github.com/voocel/litellm/otel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/voocel/codebot/internal/infra/config"
)

// Langfuse's attributes, https://langfuse.com/integrations/native/opentelemetry.
const (
	attrTraceName       = "langfuse.trace.name"
	attrTraceTags       = "langfuse.trace.tags"
	attrEnvironment     = "langfuse.environment"
	attrSession         = "langfuse.session.id"
	attrUser            = "langfuse.user.id"
	attrType            = "langfuse.observation.type"
	attrInput           = "langfuse.observation.input"
	attrOutput          = "langfuse.observation.output"
	attrLevel           = "langfuse.observation.level"
	attrStatusMessage   = "langfuse.observation.status_message"
	attrMetadata        = "langfuse.observation.metadata."
	attrUsage           = "langfuse.observation.usage_details"
	attrCost            = "langfuse.observation.cost_details"
	attrCompletionStart = "langfuse.observation.completion_start_time"
)

// The OpenTelemetry GenAI conventions, which Langfuse maps too.
const (
	attrOperation  = "gen_ai.operation.name"
	attrAgentName  = "gen_ai.agent.name"
	attrAgentID    = "gen_ai.agent.id"
	attrToolName   = "gen_ai.tool.name"
	attrToolCallID = "gen_ai.tool.call.id"
	attrToolArgs   = "gen_ai.tool.call.arguments"
	attrToolResult = "gen_ai.tool.call.result"
)

// Langfuse's observation types.
const (
	typeAgent     = "agent"
	typeChain     = "chain"
	typeTool      = "tool"
	typeGuardrail = "guardrail"
)

const defaultBaseURL = "https://cloud.langfuse.com"

type Tracer struct {
	tracer      trace.Tracer
	calls       litellm.Observer // the model calls
	environment string
	user        string
}

// New traces to tp the work of codebot's release.
func New(tp trace.TracerProvider, release string) *Tracer {
	t := &Tracer{tracer: tp.Tracer("codebot"), environment: environment(release)}
	if u, err := user.Current(); err == nil {
		t.user = u.Username
	}
	t.calls = litellmotel.New(tp.Tracer("litellm"), litellmotel.WithCaptureContent(true), litellmotel.WithSpanAttributes(t.common))
	return t
}

// Observer observes the calls of a model priced at pricing, nil when
// unknown, for litellm.WithObservers.
func (t *Tracer) Observer(pricing *catalog.Pricing) litellm.Observer {
	if t == nil {
		return nil
	}
	return generations{t.calls, pricing}
}

// Setup traces to the Langfuse project cfg names, release being codebot's
// version. Without its keys the Tracer is nil and shutdown does nothing;
// shutdown sends what is left.
func Setup(cfg config.LangfuseConfig, release string) (*Tracer, func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	switch {
	case cfg.PublicKey == "" && cfg.SecretKey == "":
		return nil, noop, nil
	case cfg.PublicKey == "" || cfg.SecretKey == "":
		return nil, noop, errors.New("langfuse: set both public_key and secret_key")
	}
	exporter, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(strings.TrimRight(cmp.Or(cfg.BaseURL, defaultBaseURL), "/")+"/api/public/otel/v1/traces"),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(cfg.PublicKey+":"+cfg.SecretKey)),
			// Without it, Langfuse takes up to ten minutes to show a trace.
			"x-langfuse-ingestion-version": "4",
		}),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
	)
	if err != nil {
		return nil, noop, fmt.Errorf("langfuse: %w", err)
	}
	// The SDK reports failed exports here; log goes where the frontend
	// keeps it, off the screen.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { log.Printf("langfuse: %v", err) }))
	// Langfuse takes service.version for each observation's version.
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", "codebot"),
		attribute.String("service.version", release),
	))
	if err != nil {
		return nil, noop, fmt.Errorf("langfuse: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(batches{redacting{exporter}, maxBatchBytes}), sdktrace.WithResource(res))
	return New(tp, release), tp.Shutdown, nil
}

type conversationKey struct{}

type conversation struct{ id, frontend string }

// WithConversation marks the work done in ctx as conversation id's, held in
// frontend: its session in Langfuse, and a tag of its traces.
func WithConversation(ctx context.Context, id, frontend string) context.Context {
	return context.WithValue(ctx, conversationKey{}, conversation{id, frontend})
}

// environment keeps the traces of codebot's own development, any build but
// a release, apart from those of its use.
func environment(release string) string {
	if releaseVersion.MatchString(release) {
		return "production"
	}
	return "development"
}

var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// common are the attributes of every observation made in ctx.
func (t *Tracer) common(ctx context.Context) []attribute.KeyValue {
	out := []attribute.KeyValue{attribute.String(attrEnvironment, t.environment)}
	if c, ok := ctx.Value(conversationKey{}).(conversation); ok {
		out = append(out, attribute.String(attrSession, c.id))
	}
	if t.user != "" {
		out = append(out, attribute.String(attrUser, t.user))
	}
	return out
}

// start begins an observation; at the root of a trace, it names the trace.
func (t *Tracer) start(ctx context.Context, name, typ, traceName string, attrs ...attribute.KeyValue) (context.Context, *Observation) {
	attrs = append(attrs, attribute.String(attrType, typ))
	attrs = append(attrs, t.common(ctx)...)
	if !trace.SpanContextFromContext(ctx).IsValid() {
		attrs = append(attrs, attribute.String(attrTraceName, traceName))
		if c, ok := ctx.Value(conversationKey{}).(conversation); ok && c.frontend != "" {
			attrs = append(attrs, attribute.StringSlice(attrTraceTags, []string{c.frontend}))
		}
	}
	ctx, span := t.tracer.Start(ctx, name, trace.WithAttributes(attrs...))
	return ctx, &Observation{span}
}

// Turn observes a user's turn: input is what the user said, meta what the
// turn ran with.
func (t *Tracer) Turn(ctx context.Context, input string, meta map[string]string) (context.Context, *Observation) {
	if t == nil {
		return ctx, nil
	}
	attrs := []attribute.KeyValue{
		attribute.String(attrOperation, "invoke_agent"),
		attribute.String(attrAgentName, "codebot"),
		attribute.String(attrInput, input),
	}
	for k, v := range meta {
		if v != "" {
			attrs = append(attrs, attribute.String(attrMetadata+k, v))
		}
	}
	return t.start(ctx, "invoke_agent codebot", typeAgent, "turn", attrs...)
}

// Work observes work outside a turn, such as compacting, under its name.
func (t *Tracer) Work(ctx context.Context, name, input string) (context.Context, *Observation) {
	if t == nil {
		return ctx, nil
	}
	var attrs []attribute.KeyValue
	if input != "" {
		attrs = append(attrs, attribute.String(attrInput, input))
	}
	return t.start(ctx, name, typeChain, name, attrs...)
}

// SubAgent observes a sub-agent's run, the run id of agent on task, for
// agentcore/subagent.Agent.WrapRun.
func (t *Tracer) SubAgent(ctx context.Context, agent, id, task string) (context.Context, func(output string, err error)) {
	if t == nil {
		return ctx, func(string, error) {}
	}
	ctx, o := t.start(ctx, "invoke_agent "+agent, typeAgent, agent,
		attribute.String(attrOperation, "invoke_agent"),
		attribute.String(attrAgentName, agent),
		attribute.String(attrAgentID, id),
		attribute.String(attrInput, task),
	)
	return ctx, o.End
}

// Observation is a piece of work under way. A nil Observation records
// nothing.
type Observation struct {
	span trace.Span
}

// End ends the observation with its output and how it ended.
func (o *Observation) End(output string, err error) {
	if o == nil {
		return
	}
	if output != "" {
		o.span.SetAttributes(attribute.String(attrOutput, output))
	}
	setOutcome(o.span, err)
	o.span.End()
}

// setOutcome marks the span with err: a stop the user or the turn limit
// made is a warning, anything else an error.
func setOutcome(span trace.Span, err error) {
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled), errors.Is(err, agentcore.ErrMaxTurns):
		span.SetAttributes(attribute.String(attrLevel, "WARNING"), attribute.String(attrStatusMessage, err.Error()))
	default:
		span.SetAttributes(attribute.String(attrLevel, "ERROR"), attribute.String(attrStatusMessage, err.Error()))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}
