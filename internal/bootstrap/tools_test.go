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

// Every guarantee beyond Name/Schema/Execute is an optional interface found by
// type assertion, so any decorator over a tool silently drops the lot and
// nothing fails loudly. Cross-cutting behaviour belongs in middleware.
//
// This covers buildTools' output only; a decorator added further up the boot
// path is out of its reach.
func TestBuiltToolsKeepOptionalCapabilities(t *testing.T) {
	t.Parallel()

	built := buildTools("/tmp/ws", defaultToolFactories(tools.NewFileReadState(), nil))
	byName := make(map[string]agentcore.Tool, len(built))
	for _, tool := range built {
		byName[tool.Name()] = tool
	}

	for _, name := range []string{"edit", "write"} {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("%s missing from the default toolset", name)
		}
		if _, ok := tool.(agentcore.Validator); !ok {
			t.Errorf("%s lost Validator — read-before-write is no longer enforced", name)
		}
		if _, ok := tool.(agentcore.Previewer); !ok {
			t.Errorf("%s lost Previewer — approval shows no diff", name)
		}
	}
	for _, name := range []string{"read", "grep", "glob", "ls"} {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("%s missing from the default toolset", name)
		}
		if _, ok := tool.(agentcore.ConcurrencySafeTool); !ok {
			t.Errorf("%s lost ConcurrencySafe — it will run serially", name)
		}
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
