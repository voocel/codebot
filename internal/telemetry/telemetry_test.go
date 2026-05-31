package telemetry

import (
	"context"
	"testing"

	"github.com/voocel/codebot/internal/config"
)

func TestSetupDisabled(t *testing.T) {
	hook, shutdown, err := Setup(context.Background(), config.TelemetryConfig{Enabled: false})
	if err != nil {
		t.Fatalf("disabled setup err: %v", err)
	}
	if hook != nil {
		t.Fatal("disabled telemetry must return a nil hook")
	}
	if shutdown == nil {
		t.Fatal("shutdown must be a non-nil noop")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown err: %v", err)
	}
}

func TestSetupEnabledNoEndpoint(t *testing.T) {
	hook, _, err := Setup(context.Background(), config.TelemetryConfig{Enabled: true})
	if err != nil {
		t.Fatalf("setup err: %v", err)
	}
	if hook != nil {
		t.Fatal("an empty endpoint must disable telemetry even when Enabled")
	}
}

func TestSetupEnabledReturnsHook(t *testing.T) {
	hook, shutdown, err := Setup(context.Background(), config.TelemetryConfig{
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
	if shutdown == nil {
		t.Fatal("shutdown must be non-nil")
	}
	// No spans were produced, so shutdown flushes nothing and must not hang or
	// error despite the endpoint being unreachable.
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown err: %v", err)
	}
}
