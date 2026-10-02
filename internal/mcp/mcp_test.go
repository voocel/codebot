package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	sdkmcp "github.com/voocel/mcp-sdk-go/protocol"
	sdkserver "github.com/voocel/mcp-sdk-go/server"
	sdkhttp "github.com/voocel/mcp-sdk-go/transport/streamhttp"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/permission"
)

func TestExpandEnv(t *testing.T) {
	t.Setenv("TEST_MCP_VAR", "hello")

	result := expandEnv(map[string]string{
		"KEY":  "${TEST_MCP_VAR}_world",
		"MISS": "${NONEXISTENT_MCP_TEST_VAR}",
	})

	want := map[string]string{"KEY": "hello_world", "MISS": ""}
	for k, v := range want {
		entry := k + "=" + v
		if !slices.Contains(result, entry) {
			t.Errorf("expected %s in result", entry)
		}
	}
}

func TestMCPToolAdapter(t *testing.T) {
	t.Parallel()
	c := &Client{name: "srv"}

	t.Run("basic", func(t *testing.T) {
		t.Parallel()
		mt := sdkmcp.Tool{
			Name: "my-tool", Description: "A test tool",
			InputSchema: map[string]any{"type": "object"},
		}
		tool := newTool(c, &mt)

		if tool.Name != "mcp__srv__my-tool" || tool.Label != "my-tool" || tool.Description != "A test tool" || tool.Schema["type"] != "object" {
			t.Errorf("tool = %+v", tool)
		}
	})

	t.Run("title_as_label", func(t *testing.T) {
		t.Parallel()
		mt := sdkmcp.Tool{
			Name: "x", Title: "Display Name", Description: "d",
			InputSchema: map[string]any{"type": "object"},
		}
		if tool := newTool(c, &mt); tool.Label != "Display Name" {
			t.Errorf("Label = %q", tool.Label)
		}
	})

	t.Run("nil_schema_fallback", func(t *testing.T) {
		t.Parallel()
		mt := sdkmcp.Tool{Name: "x", Description: "d"}
		if tool := newTool(c, &mt); tool.Schema["type"] != "object" {
			t.Errorf("Schema = %v", tool.Schema)
		}
	})

	t.Run("permission_metadata_from_annotations", func(t *testing.T) {
		t.Parallel()
		mt := sdkmcp.Tool{
			Name:        "lookup",
			Description: "Read data from remote source",
			Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: boolPtr(true)},
		}
		meta := permissionOf(&mt)
		if meta.Capability != permission.CapabilityRead || meta.SummaryHint != "lookup" || meta.KeyPrefix != "mcp" {
			t.Fatalf("capability = %q, want %q", meta.Capability, permission.CapabilityRead)
		}
	})

	t.Run("permission_metadata_heuristic_network", func(t *testing.T) {
		t.Parallel()
		mt := sdkmcp.Tool{
			Name:        "web-search",
			Description: "Search the web",
		}
		meta := permissionOf(&mt)
		if meta.Capability != permission.CapabilityNetwork || meta.Reason == "" {
			t.Fatalf("capability = %q, want %q", meta.Capability, permission.CapabilityNetwork)
		}
	})
}

func boolPtr(v bool) *bool { return &v }

func TestClientUsesStatelessSDK(t *testing.T) {
	backend := sdkserver.New(&sdkserver.Options{
		Impl:         sdkmcp.Implementation{Name: "test-server", Version: "1.0.0"},
		Instructions: "test instructions",
		PageSize:     1,
	})
	handler := func(context.Context, *sdkserver.CallRequest) (sdkmcp.ToolResponse, error) {
		return sdkmcp.NewToolResultText("ok"), nil
	}
	backend.AddTool(&sdkmcp.Tool{Name: "alpha"}, handler)
	backend.AddTool(&sdkmcp.Tool{Name: "beta"}, handler)

	httpServer := httptest.NewServer(sdkhttp.NewHandler(backend, nil))
	defer httpServer.Close()

	changed := make(chan struct{}, 1)
	connectCtx, cancelConnect := context.WithCancel(t.Context())
	client, err := connect(connectCtx, "test", config.MCPServer{Type: "http", URL: httpServer.URL}, func() {
		changed <- struct{}{}
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	// The caller's context bounds connection setup, not the owned client
	// lifetime. This mirrors runtime reload, whose setup context is short-lived.
	cancelConnect()

	if got := client.Instructions(); got != "test instructions" {
		t.Fatalf("instructions = %q", got)
	}
	tools, err := client.ListTools(t.Context())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("tool count = %d, want 2", len(tools))
	}
	if got := []string{tools[0].Name, tools[1].Name}; !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("tools = %v", got)
	}
	res, err := newTool(client, tools[0]).Run(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if got := res.Text(); got != "ok" {
		t.Fatalf("tool result = %q", got)
	}

	backend.AddTool(&sdkmcp.Tool{Name: "gamma"}, handler)
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tools/list_changed")
	}
}

func TestManagerReconfigureClearsFailures(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	defer m.Close()

	m.failures["broken"] = "boom"

	errs := m.Reconfigure(t.Context(), nil)
	if len(errs) != 0 {
		t.Fatalf("expected no reconfigure errors, got %v", errs)
	}
	if len(m.failures) != 0 {
		t.Fatalf("expected failures to be cleared, got %v", m.failures)
	}
}

// --- helpers ---

// TestSortedClientsDeterministic guards the prompt-cache invariant: tools and
// instructions must serialize in the same byte order across refreshes, so
// client iteration must not depend on map order.
func TestSortedClientsDeterministic(t *testing.T) {
	m := NewManager(nil)
	for _, name := range []string{"zeta", "alpha", "mid", "beta"} {
		m.clients[name] = &Client{name: name}
	}
	want := []string{"alpha", "beta", "mid", "zeta"}
	for range 50 {
		got := m.sortedClients()
		if len(got) != len(want) {
			t.Fatalf("sortedClients len=%d want %d", len(got), len(want))
		}
		for i, c := range got {
			if c.name != want[i] {
				t.Fatalf("sortedClients[%d]=%s want %s", i, c.name, want[i])
			}
		}
	}
}
