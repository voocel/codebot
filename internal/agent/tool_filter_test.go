package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/voocel/agentcore"
)

// fakeTool implements the bits of agentcore.Tool the filter cares about. The
// full interface has many methods (Schema/Description/Execute), so each test
// uses one concrete name per tool to keep cases readable.
type fakeTool struct{ name string }

func (t *fakeTool) Name() string           { return t.name }
func (t *fakeTool) Label() string          { return t.name }
func (t *fakeTool) Description() string    { return "" }
func (t *fakeTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *fakeTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func mkTools(names ...string) []agentcore.Tool {
	out := make([]agentcore.Tool, 0, len(names))
	for _, n := range names {
		out = append(out, &fakeTool{name: n})
	}
	return out
}

func names(in []agentcore.Tool) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		out = append(out, t.Name())
	}
	return out
}

func contains(xs []string, x string) bool {
	return slices.Contains(xs, x)
}

// fullMainPool mirrors what the main agent's tool list looks like AS HANDED
// TO buildSubAgents. Skill and tool_search are absent here on purpose —
// at bootstrap they are appended after buildSubAgents runs (see
// assemble_session.go) so the filter never sees them. Keeping the test pool
// in sync with the real input keeps the asyncAgentAllowed list honest.
func fullMainPool() []agentcore.Tool {
	return mkTools(
		"read", "write", "edit", "bash", "glob", "grep", "ls",
		"web_search", "web_fetch",
		"task_create", "task_get", "task_update", "task_list", "task_output", "task_stop",
		"enter_plan_mode", "exit_plan_mode",
		"ask_user",
		"cron_create", "cron_delete", "cron_list",
		"subagent",
		"send_message",
		"team_create",
		"team_dismiss",
		"mcp__github__create_issue",
	)
}

// Subagent itself must always be stripped, no matter the options. Without
// this guard a sub-agent could spawn its own sub-agents and the tree would
// grow unbounded.
func TestFilter_AlwaysDropsSubagentTool(t *testing.T) {
	for _, opts := range []FilterOpts{
		{IsBuiltIn: true, IsAsync: false, AllowMCP: true},
		{IsBuiltIn: true, IsAsync: true, AllowMCP: true},
		{IsBuiltIn: false, IsAsync: false, AllowMCP: true},
		{IsBuiltIn: false, IsAsync: true, AllowMCP: true},
	} {
		got := names(FilterToolsForAgent(fullMainPool(), opts))
		if contains(got, "subagent") {
			t.Errorf("subagent leaked through with opts %+v", opts)
		}
	}
}

// Built-in sync agent (general-purpose-like): keeps the broad coding toolset
// but loses every tool the main agent reserves for itself.
func TestFilter_BuiltInSync_GeneralPurpose(t *testing.T) {
	got := names(FilterToolsForAgent(fullMainPool(), FilterOpts{
		IsBuiltIn: true,
		IsAsync:   false,
		AllowMCP:  true,
	}))

	// Must keep: full coding surface.
	for _, want := range []string{
		"read", "write", "edit", "bash", "glob", "grep", "ls",
		"web_search", "web_fetch",
		"task_get", "task_list", "task_output", "cron_list",
		"mcp__github__create_issue",
	} {
		if !contains(got, want) {
			t.Errorf("general-purpose pool missing %q", want)
		}
	}

	// Must drop: main-agent-only and recursive spawn.
	for _, deny := range []string{
		"ask_user",
		"enter_plan_mode", "exit_plan_mode",
		"task_create", "task_update", "task_stop",
		"cron_create", "cron_delete",
		"subagent",
		"send_message",
		"team_create",
		"team_dismiss",
	} {
		if contains(got, deny) {
			t.Errorf("general-purpose pool should not contain %q", deny)
		}
	}
}

// Built-in async + read-only (explore-like): allow-list narrows to async
// tools; ExtraDisallowed strips write/edit/bash from that already-narrow set.
func TestFilter_BuiltInAsync_ExploreReadOnly(t *testing.T) {
	got := names(FilterToolsForAgent(fullMainPool(), FilterOpts{
		IsBuiltIn:       true,
		IsAsync:         true,
		AllowMCP:        true,
		ExtraDisallowed: []string{"write", "edit", "bash"},
	}))

	// Must keep: read-only async surface.
	for _, want := range []string{
		"read", "glob", "grep", "ls",
		"web_search", "web_fetch",
		"mcp__github__create_issue",
	} {
		if !contains(got, want) {
			t.Errorf("explore pool missing %q", want)
		}
	}

	// Must drop: mutating + main-agent-only + anything not on the async
	// allow-list (e.g. task_get is not async-allowed even though it's
	// otherwise permissible for sync sub-agents).
	for _, deny := range []string{
		"write", "edit", "bash",
		"ask_user",
		"task_create", "task_update", "task_stop",
		"task_get", "task_list", "task_output",
		"cron_create", "cron_delete", "cron_list",
		"enter_plan_mode", "exit_plan_mode",
		"subagent",
		"send_message",
		"team_create",
		"team_dismiss",
	} {
		if contains(got, deny) {
			t.Errorf("explore pool should not contain %q", deny)
		}
	}
}

// User-defined sync agent: today identical to built-in sync because
// customAgentDisallowed is empty — but the predicate path is exercised so a
// regression that empties allAgentDisallowed by mistake still trips a test.
func TestFilter_CustomSync(t *testing.T) {
	got := names(FilterToolsForAgent(fullMainPool(), FilterOpts{
		IsBuiltIn: false,
		IsAsync:   false,
		AllowMCP:  true,
	}))
	for _, deny := range []string{"ask_user", "task_create", "subagent"} {
		if contains(got, deny) {
			t.Errorf("custom sync pool should not contain %q", deny)
		}
	}
}

func TestFilter_CustomAsync(t *testing.T) {
	got := names(FilterToolsForAgent(fullMainPool(), FilterOpts{
		IsBuiltIn: false,
		IsAsync:   true,
		AllowMCP:  true,
	}))
	for _, deny := range []string{
		"task_get", "task_list", "task_output", "cron_list",
		"ask_user", "subagent",
	} {
		if contains(got, deny) {
			t.Errorf("custom async pool should not contain %q", deny)
		}
	}
	if !contains(got, "read") {
		t.Error("custom async pool should keep read")
	}
}

// MCP tools bypass the allow/deny lists when AllowMCP is true. When false,
// they are dropped regardless. This split lets a user-defined agent that
// declares zero MCP intent stay isolated from the parent's MCP surface.
func TestFilter_MCPGate(t *testing.T) {
	pool := mkTools("read", "mcp__a__x", "mcp__b__y", "ask_user")

	allowed := names(FilterToolsForAgent(pool, FilterOpts{
		IsBuiltIn: true, IsAsync: true, AllowMCP: true,
	}))
	if !contains(allowed, "mcp__a__x") || !contains(allowed, "mcp__b__y") {
		t.Errorf("MCP tools dropped despite AllowMCP=true: %v", allowed)
	}

	denied := names(FilterToolsForAgent(pool, FilterOpts{
		IsBuiltIn: true, IsAsync: true, AllowMCP: false,
	}))
	if contains(denied, "mcp__a__x") || contains(denied, "mcp__b__y") {
		t.Errorf("MCP tools leaked through despite AllowMCP=false: %v", denied)
	}
}

// Stable identity: the filter returns instances from the input slice; it
// does not wrap or substitute. Important because sub-agents may carry
// per-instance state (e.g. independent FileReadState built upstream) and
// the filter must not unwittingly hand back a different object.
func TestFilter_PreservesInstances(t *testing.T) {
	read := &fakeTool{name: "read"}
	in := []agentcore.Tool{read}
	out := FilterToolsForAgent(in, FilterOpts{IsBuiltIn: true})
	if len(out) != 1 || out[0] != read {
		t.Fatalf("filter must preserve tool instance identity, got %#v", out)
	}
}

// BuildToolPool must return INDEPENDENT read/write/edit instances on each
// call. Two sub-agent kinds (e.g. explore and general-purpose) share their input
// tool list — if they also share the read instance, a read in one silences a
// missing read-before-write check in the other. The pool is the linchpin of
// that invariant; this test pins it down.
//
// Conversely, tools the pool does NOT replace (bash here) should alias
// across calls — they are stateless from the pool's perspective and
// duplicating them would diverge sub-agents from the parent's output-limit
// wrapper.
func TestBuildToolPool_PerCallIndependence(t *testing.T) {
	main := []agentcore.Tool{&fakeTool{name: "read"}, &fakeTool{name: "bash"}}

	a := BuildToolPool("/tmp/ws", main, nil)
	b := BuildToolPool("/tmp/ws", main, nil)

	readA, readB := findByName(a, "read"), findByName(b, "read")
	if readA == nil || readB == nil {
		t.Fatal("read missing from pool output")
	}
	if readA == readB {
		t.Fatal("two pool calls returned the same read instance; FileReadState would be shared across sub-agents")
	}

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
