package telemetry

import (
	"context"
	"testing"

	"github.com/voocel/codebot/internal/infra/config"
)

// A typo in the attribute keys silently breaks session grouping on the
// backend.
func TestSessionSpanAttributes(t *testing.T) {
	got := sessionSpanAttributes("sess-42")
	m := make(map[string]string, len(got))
	for _, kv := range got {
		m[string(kv.Key)] = kv.Value.AsString()
	}
	if m["langfuse.session.id"] != "sess-42" {
		t.Errorf("langfuse.session.id = %q, want sess-42", m["langfuse.session.id"])
	}
	if m["session.id"] != "sess-42" {
		t.Errorf("session.id = %q, want sess-42 (generic fallback)", m["session.id"])
	}
}

// Telemetry stays off, with a noop shutdown, unless it is enabled and has an
// endpoint to send to.
func TestSetupDisabled(t *testing.T) {
	for _, cfg := range []config.TelemetryConfig{{Enabled: false}, {Enabled: true}} {
		hook, tracer, shutdown, err := Setup(context.Background(), cfg)
		if err != nil || hook != nil || tracer != nil || shutdown == nil {
			t.Fatalf("%+v: hook %v, tracer %v, shutdown nil %v, err %v", cfg, hook, tracer, shutdown == nil, err)
		}
		if err := shutdown(context.Background()); err != nil {
			t.Fatalf("%+v: noop shutdown err: %v", cfg, err)
		}
	}
}

func TestSetupEnabledReturnsHook(t *testing.T) {
	hook, tracer, shutdown, err := Setup(context.Background(), config.TelemetryConfig{
		Enabled:   true,
		Endpoint:  "https://example.test/api/public/otel",
		PublicKey: "pk",
		SecretKey: "sk",
	})
	if err != nil {
		t.Fatalf("enabled setup err: %v", err)
	}
	if hook == nil || tracer == nil || shutdown == nil {
		t.Fatalf("enabled telemetry must return a hook, a tracer and a shutdown: %v, %v, nil shutdown %v", hook, tracer, shutdown == nil)
	}
	tracer.SetSession("sess-42")
	// No spans were produced, so shutdown flushes nothing and must not hang or
	// error despite the endpoint being unreachable.
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown err: %v", err)
	}
}
