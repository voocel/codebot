package telemetry

import (
	"context"
	"testing"

	"github.com/voocel/codebot/internal/config"
)

// The exact attribute keys matter: a typo silently breaks session grouping on
// the backend (the very bug this feature fixes), so pin them down.
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

func TestSessionSpanAttributesEmptyIsNil(t *testing.T) {
	if got := sessionSpanAttributes(""); got != nil {
		t.Errorf("empty session id must yield nil (no tagging), got %v", got)
	}
}

func TestSetupDisabled(t *testing.T) {
	hook, tracer, shutdown, err := Setup(context.Background(), config.TelemetryConfig{Enabled: false})
	if err != nil {
		t.Fatalf("disabled setup err: %v", err)
	}
	if hook != nil {
		t.Fatal("disabled telemetry must return a nil hook")
	}
	if tracer != nil {
		t.Fatal("disabled telemetry must return a nil tracer")
	}
	if shutdown == nil {
		t.Fatal("shutdown must be a non-nil noop")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown err: %v", err)
	}
}

func TestSetupEnabledNoEndpoint(t *testing.T) {
	hook, _, _, err := Setup(context.Background(), config.TelemetryConfig{Enabled: true})
	if err != nil {
		t.Fatalf("setup err: %v", err)
	}
	if hook != nil {
		t.Fatal("an empty endpoint must disable telemetry even when Enabled")
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
	if hook == nil {
		t.Fatal("enabled telemetry must return a hook")
	}
	if tracer == nil {
		t.Fatal("enabled telemetry must return a tracer")
	}
	tracer.BindSession(func() string { return "sess-42" })
	if shutdown == nil {
		t.Fatal("shutdown must be non-nil")
	}
	// No spans were produced, so shutdown flushes nothing and must not hang or
	// error despite the endpoint being unreachable.
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown err: %v", err)
	}
}
