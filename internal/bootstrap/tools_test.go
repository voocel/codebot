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

	built := buildTools("/tmp/ws", defaultToolFactories(tools.NewFileReadState()))
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

// Sub-agents must not share read/write/edit instances — sharing would let a
// read in one sub-agent silence a missing read-before-write check in
// another. Compare the address of the read tool produced by two pool calls:
// they must differ. This guards the per-agent independence invariant in
// buildSubAgentTool against well-meaning refactors that hoist the pool call
// out of the three agent constructors.
func TestSubagentToolPool_PerCallIndependence(t *testing.T) {
	t.Parallel()

	main := []agentcore.Tool{&fakeTool{name: "read"}, &fakeTool{name: "bash"}}

	a := subagentToolPool("/tmp/ws", main)
	b := subagentToolPool("/tmp/ws", main)

	readA, readB := findByName(a, "read"), findByName(b, "read")
	if readA == nil || readB == nil {
		t.Fatal("read missing from pool output")
	}
	if readA == readB {
		t.Fatal("two pool calls returned the same read instance; FileReadState would be shared across sub-agents")
	}

	// Tools NOT replaced by the pool (e.g. bash) intentionally do alias.
	// Bash is stateless from the pool's perspective — duplicating it would
	// waste allocations and divorce sub-agents from the parent's output
	// truncation wrapper.
	bashA, bashB := findByName(a, "bash"), findByName(b, "bash")
	if bashA != bashB {
		t.Errorf("bash instance should be reused across pool calls, got %p vs %p", bashA, bashB)
	}
}

func findByName(in []agentcore.Tool, name string) agentcore.Tool {
	for _, t := range in {
		if t.Name() == name {
			return t
		}
	}
	return nil
}
