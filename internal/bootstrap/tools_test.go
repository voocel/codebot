package bootstrap

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/tools"
)

type fakeTool struct {
	name string
}

func (t *fakeTool) Name() string        { return t.name }
func (t *fakeTool) Description() string { return "fake" }
func (t *fakeTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *fakeTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func TestBuildToolsDefaults(t *testing.T) {
	t.Parallel()

	built := buildTools("/tmp/ws", defaultToolFactories(tools.NewFileReadState(), nil))
	if len(built) < 7 {
		t.Fatalf("expected at least 7 default tools, got %d", len(built))
	}
}

func TestBuildToolsCustom(t *testing.T) {
	t.Parallel()

	factories := []ToolFactory{
		func(_ string) agentcore.Tool { return &fakeTool{name: "one"} },
		func(_ string) agentcore.Tool { return &fakeTool{name: "two"} },
	}
	tools := buildTools("/tmp/ws", factories)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name() != "one" || tools[1].Name() != "two" {
		t.Fatalf("unexpected tool order: %s, %s", tools[0].Name(), tools[1].Name())
	}
}

func TestBuildToolsEmptyWhenAllFactoriesInvalid(t *testing.T) {
	t.Parallel()

	factories := []ToolFactory{
		nil,
		func(_ string) agentcore.Tool { return nil },
	}
	tools := buildTools("/tmp/ws", factories)
	if len(tools) != 0 {
		t.Fatalf("expected empty tools for invalid factories, got %d", len(tools))
	}
}
