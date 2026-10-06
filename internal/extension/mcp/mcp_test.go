package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/voocel/mcp-sdk-go/protocol"
	sdkserver "github.com/voocel/mcp-sdk-go/server"
	sdkhttp "github.com/voocel/mcp-sdk-go/transport/streamhttp"

	"github.com/voocel/codebot/internal/agent/permission"
	"github.com/voocel/codebot/internal/infra/config"
)

func TestToolName(t *testing.T) {
	if got := toolName("acme-tools_db", "query"); got != "mcp__acme-tools_db__query" {
		t.Errorf("got %s", got)
	}
	// Names the vendors refuse, made alike, stay apart.
	dotted, dashed := toolName("acme.tools_db", "query"), toolName("acme-tools_db", "query")
	if dotted == dashed || !strings.HasPrefix(dotted, "mcp__acme-tools_db__query_") {
		t.Errorf("dotted %s, dashed %s", dotted, dashed)
	}
	long := toolName("server", strings.Repeat("x", 80))
	if len(long) != maxToolName || long == toolName("server", strings.Repeat("x", 81)) {
		t.Errorf("long names %s", long)
	}
	if a, b := toolName("s", strings.Repeat("x", 70)+".a"), toolName("s", strings.Repeat("x", 70)+"-a"); a == b {
		t.Errorf("long names alike: %s", a)
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

func TestManagerConfigureClearsFailures(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	defer m.Close()

	m.failures["broken"] = "boom"

	errs := m.Configure(t.Context(), nil)
	if len(errs) != 0 {
		t.Fatalf("expected no configure errors, got %v", errs)
	}
	if len(m.failures) != 0 {
		t.Fatalf("expected failures to be cleared, got %v", m.failures)
	}
}

// A server configured as before stays connected through Configure; one
// changed reconnects, one gone disconnects.
func TestManagerConfigureKeepsWhatDidNotChange(t *testing.T) {
	backend := sdkserver.New(&sdkserver.Options{Impl: sdkmcp.Implementation{Name: "s", Version: "1"}})
	srv := httptest.NewServer(sdkhttp.NewHandler(backend, nil))
	defer srv.Close()
	m := NewManager(nil)
	defer m.Close()

	cfg := config.MCPServer{Type: "http", URL: srv.URL}
	changed := config.MCPServer{Type: "http", URL: srv.URL, Headers: map[string]string{"X-Test": "1"}}
	if errs := m.Configure(t.Context(), map[string]config.MCPServer{"kept": cfg, "changed": cfg, "gone": cfg}); len(errs) > 0 {
		t.Fatal(errs)
	}
	kept, before := m.clients["kept"], m.clients["changed"]
	if errs := m.Configure(t.Context(), map[string]config.MCPServer{"kept": cfg, "changed": changed}); len(errs) > 0 {
		t.Fatal(errs)
	}
	if m.clients["kept"] != kept {
		t.Error("an unchanged server reconnected")
	}
	if c := m.clients["changed"]; c == nil || c == before {
		t.Error("a changed server kept its connection")
	}
	if _, ok := m.clients["gone"]; ok {
		t.Error("a server no longer configured stayed")
	}
}

// What a server that fails to start says on stderr explains the failure,
// and stays off the terminal.
func TestConnectTellsWhatTheServerSaid(t *testing.T) {
	_, err := connect(t.Context(), "broken", config.MCPServer{Command: "sh", Args: []string{"-c", "echo first >&2; echo 404 Not Found >&2; exit 1"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "first\n404 Not Found") {
		t.Fatalf("err = %v", err)
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
