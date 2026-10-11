package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/catalog"
	"github.com/voocel/litellm/litellmtest"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/voocel/codebot/internal/infra/config"
)

// record returns a Tracer whose spans end up in the recorder.
func record(t *testing.T) (*Tracer, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	return New(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)), "1.2.3"), rec
}

func attrs(s sdktrace.ReadOnlySpan) map[string]string {
	out := map[string]string{}
	for _, kv := range s.Attributes() {
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

func named(t *testing.T, rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range rec.Ended() {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no span %q", name)
	return nil
}

// A turn is a trace of the conversation's session: an agent with what the
// user said and the answer, the tool calls under it with their approvals,
// and the sub-agents it ran.
func TestATurnIsATrace(t *testing.T) {
	tracer, rec := record(t)
	ctx := WithConversation(context.Background(), "s1", "tui")
	ctx, turn := tracer.Turn(ctx, "fix the bug", map[string]string{"mode": "balanced", "effort": ""})
	_, _ = tracer.ToolMiddleware()(ctx, agentcore.ToolCall{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"go test"}`)},
		func(ctx context.Context, _ agentcore.ToolCall) (agentcore.Result, error) {
			_, approval := tracer.Approval(ctx, "go test")
			approval.End("allow_once", nil)
			return agentcore.TextResult("ok"), nil
		})
	// The sub-agent's own run stands for the call that started it.
	_, _ = tracer.ToolMiddleware()(ctx, agentcore.ToolCall{ID: "c2", Name: "subagent"}, func(ctx context.Context, _ agentcore.ToolCall) (agentcore.Result, error) {
		_, end := tracer.SubAgent(ctx, "explore", "explore#1", "find it")
		end("found it", nil)
		return agentcore.TextResult("found it"), nil
	})
	turn.End("fixed", nil)

	root := named(t, rec, "invoke_agent codebot")
	want := map[string]string{
		"langfuse.observation.type": "agent", "langfuse.trace.name": "turn", "langfuse.trace.tags": `["tui"]`,
		"langfuse.environment": "development", "langfuse.session.id": "s1", "gen_ai.operation.name": "invoke_agent",
		"langfuse.observation.input": "fix the bug", "langfuse.observation.output": "fixed",
		"langfuse.observation.metadata.mode": "balanced",
	}
	got := attrs(root)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("turn %s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["langfuse.observation.metadata.effort"]; ok {
		t.Error("the turn holds an empty effort")
	}

	tool := named(t, rec, "execute_tool bash")
	if a := attrs(tool); tool.Parent().SpanID() != root.SpanContext().SpanID() || a["langfuse.observation.type"] != "tool" ||
		a["gen_ai.tool.call.arguments"] != `{"command":"go test"}` || a["gen_ai.tool.call.result"] != "ok" || a["langfuse.session.id"] != "s1" {
		t.Errorf("tool %v", a)
	}
	if _, ok := attrs(tool)["langfuse.trace.name"]; ok {
		t.Error("a tool call inside a turn names the trace")
	}
	approval := named(t, rec, "approval")
	if a := attrs(approval); approval.Parent().SpanID() != tool.SpanContext().SpanID() || a["langfuse.observation.type"] != "guardrail" ||
		a["langfuse.observation.input"] != "go test" || a["langfuse.observation.output"] != "allow_once" {
		t.Errorf("approval %v", a)
	}
	for _, s := range rec.Ended() {
		if s.Name() == "execute_tool subagent" {
			t.Error("the sub-agent shows twice, as a tool call too")
		}
	}
	sub := named(t, rec, "invoke_agent explore")
	if a := attrs(sub); sub.Parent().SpanID() != root.SpanContext().SpanID() || a["langfuse.observation.type"] != "agent" ||
		a["gen_ai.agent.id"] != "explore#1" || a["langfuse.observation.input"] != "find it" || a["langfuse.observation.output"] != "found it" {
		t.Errorf("sub-agent %v", a)
	}
}

// A stop the user or the turn limit made is a warning; a failure, and a
// tool call that failed, an error.
func TestHowWorkEnded(t *testing.T) {
	tracer, rec := record(t)
	for name, err := range map[string]error{"cancelled": context.Canceled, "limited": agentcore.ErrMaxTurns, "failed": errors.New("disk full")} {
		_, work := tracer.Work(context.Background(), name, "")
		work.End("", err)
	}
	_, _ = tracer.ToolMiddleware()(context.Background(), agentcore.ToolCall{Name: "read"}, func(context.Context, agentcore.ToolCall) (agentcore.Result, error) {
		return agentcore.ErrorResult("no such file"), nil
	})
	_, _ = tracer.ToolMiddleware()(context.Background(), agentcore.ToolCall{Name: "write"}, func(context.Context, agentcore.ToolCall) (agentcore.Result, error) {
		return agentcore.Result{}, errors.New("nobody can approve this")
	})
	if got := attrs(named(t, rec, "execute_tool write"))["gen_ai.tool.call.result"]; got != "nobody can approve this" {
		t.Errorf("a refused call's result %q, want what the model gets", got)
	}
	for name, level := range map[string]string{"cancelled": "WARNING", "limited": "WARNING", "failed": "ERROR", "execute_tool read": "ERROR", "execute_tool write": "ERROR"} {
		s := named(t, rec, name)
		if got := attrs(s)["langfuse.observation.level"]; got != level {
			t.Errorf("%s: level %q, want %s", name, got, level)
		}
		if (s.Status().Code == codes.Error) != (name == "failed" || name == "execute_tool write") {
			t.Errorf("%s: status %v", name, s.Status())
		}
	}
	if a := attrs(named(t, rec, "cancelled")); a["langfuse.trace.name"] != "cancelled" || a["langfuse.observation.type"] != "chain" {
		t.Errorf("work outside a turn %v", a)
	}
}

// A model call records the conversation and the response, when the response
// began, its usage as Langfuse prices it, the hour-long cache writes apart,
// and its cost at the model's pricing.
func TestAModelCall(t *testing.T) {
	tracer, rec := record(t)
	usage := litellm.Usage{InputTokens: 1000, OutputTokens: 200, ReasoningTokens: 50, CacheReadTokens: 600, CacheWriteTokens: 300, CacheWrite1hTokens: 300}
	read, write1h := 0.1, 2.0
	pricing := &catalog.Pricing{Rates: catalog.Rates{Input: 1, Output: 5, CacheRead: &read, CacheWrite1h: &write1h}}
	client, err := litellm.New(litellmtest.New(litellmtest.Reply{Blocks: []litellm.Block{litellm.Text("hi")}, Usage: usage}), litellm.WithObservers(tracer.Observer(pricing)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, turn := tracer.Turn(WithConversation(context.Background(), "s1", "acp"), "hello", nil)
	stream, err := client.Stream(ctx, litellm.Request{Model: "claude-sonnet-4-5", Messages: []litellm.Message{litellm.User(litellm.Text("hello"))}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := litellm.Collect(stream); err != nil {
		t.Fatal(err)
	}
	turn.End("hi", nil)

	gen := named(t, rec, "chat")
	a := attrs(gen)
	var details map[string]int
	if err := json.Unmarshal([]byte(a["langfuse.observation.usage_details"]), &details); err != nil {
		t.Fatalf("usage %q: %v", a["langfuse.observation.usage_details"], err)
	}
	want := map[string]int{"input": 100, "output": 150, "input_cached_tokens": 600, "input_cache_creation_1h": 300, "output_reasoning_tokens": 50}
	if !reflect.DeepEqual(details, want) {
		t.Errorf("usage %v, want %v", details, want)
	}
	var cost map[string]float64
	if err := json.Unmarshal([]byte(a["langfuse.observation.cost_details"]), &cost); err != nil {
		t.Fatalf("cost %q: %v", a["langfuse.observation.cost_details"], err)
	}
	if want := (map[string]float64{"input": 100, "output": 1000, "input_cached_tokens": 60, "input_cache_creation": 600, "total": 1760}); !reflect.DeepEqual(cost, want) {
		t.Errorf("cost %v, want %v", cost, want)
	}
	if a["langfuse.observation.completion_start_time"] == "" || a["langfuse.session.id"] != "s1" || a["gen_ai.request.model"] != "claude-sonnet-4-5" ||
		!strings.Contains(a["gen_ai.input.messages"], "hello") || !strings.Contains(a["gen_ai.output.messages"], "hi") {
		t.Errorf("model call %v", a)
	}
}

// A release's traces are its use; any other build's, codebot's development.
func TestEnvironment(t *testing.T) {
	for release, want := range map[string]string{"v1.4.0": "production", "dev": "development", "v0.0.0-20261009120000-abcdef123456+dirty": "development"} {
		if got := environment(release); got != want {
			t.Errorf("%s: %s, want %s", release, got, want)
		}
	}
}

// Credentials are cut from what is sent, wherever they are in a span; a
// span without any goes as it is.
func TestSecretsAreCutOut(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tr := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("t")
	key := "sk-ant-api03-" + strings.Repeat("a1B2", 10)
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\\nb3BlbnNzaC1rZXk=\\n-----END OPENSSH PRIVATE KEY-----"
	_, span := tr.Start(context.Background(), "leaky")
	span.SetAttributes(attribute.String("gen_ai.input.messages", `[{"content":"key `+key+` and `+pem+`"}]`), attribute.Int("n", 1))
	span.RecordError(errors.New("auth failed for AKIAABCDEFGHIJKLMNOP"))
	span.SetStatus(codes.Error, "token ghp_"+strings.Repeat("x", 36))
	span.End()
	_, clean := tr.Start(context.Background(), "clean")
	clean.SetAttributes(attribute.String("text", "disk-usage-report-for-the-task-runner"))
	clean.End()

	sent := tracetest.NewInMemoryExporter()
	if err := (redacting{sent}).ExportSpans(context.Background(), rec.Ended()); err != nil {
		t.Fatal(err)
	}
	got := sent.GetSpans()
	leaky := got[0]
	all := fmt.Sprint(leaky.Attributes, leaky.Events, leaky.Status)
	for _, secret := range []string{key, "b3BlbnNzaC1rZXk", "AKIAABCDEFGHIJKLMNOP", "ghp_"} {
		if strings.Contains(all, secret) {
			t.Errorf("%s was sent: %s", secret, all)
		}
	}
	if !strings.Contains(all, `key [redacted] and [redacted]`) {
		t.Errorf("sent %s", all)
	}
	if got[1].Attributes[0].Value.AsString() != "disk-usage-report-for-the-task-runner" {
		t.Errorf("a clean span changed: %v", got[1].Attributes)
	}
}

// A request stays under the limit; a span over it goes alone.
func TestBatchesStayUnderTheLimit(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tr := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("t")
	for _, size := range []int{400, 400, 400, 2000, 100} {
		_, span := tr.Start(context.Background(), "s")
		span.SetAttributes(attribute.String("text", strings.Repeat("x", size)))
		span.End()
	}
	sent := &tracetest.InMemoryExporter{}
	var counts []int
	b := batches{countingExporter{sent, &counts}, 1500}
	if err := b.ExportSpans(context.Background(), rec.Ended()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(counts, []int{2, 1, 1, 1}) || len(sent.GetSpans()) != 5 {
		t.Fatalf("requests of %v spans, %d sent", counts, len(sent.GetSpans()))
	}
}

type countingExporter struct {
	*tracetest.InMemoryExporter
	counts *[]int
}

func (e countingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	*e.counts = append(*e.counts, len(spans))
	return e.InMemoryExporter.ExportSpans(ctx, spans)
}

// The keys turn tracing on; one without the other is a mistake.
func TestSetup(t *testing.T) {
	tracer, shutdown, err := Setup(config.LangfuseConfig{}, "dev")
	if err != nil || tracer != nil || tracer.Observer(nil) != nil || shutdown(context.Background()) != nil {
		t.Fatalf("without keys: %v %v", tracer, err)
	}
	if _, _, err := Setup(config.LangfuseConfig{PublicKey: "pk"}, "dev"); err == nil {
		t.Fatal("a public key alone set up")
	}
	tracer, shutdown, err = Setup(config.LangfuseConfig{PublicKey: "pk", SecretKey: "sk", BaseURL: "https://langfuse.example.test/"}, "dev")
	if err != nil || tracer == nil {
		t.Fatalf("with keys: %v %v", tracer, err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
