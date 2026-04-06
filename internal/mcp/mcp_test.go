package mcp

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/voocel/agentcore/permission"
	sdkmcp "github.com/voocel/mcp-sdk-go/protocol"
)

func TestExpandEnv(t *testing.T) {
	t.Setenv("TEST_MCP_VAR", "hello")

	result := ExpandEnv(map[string]string{
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

func TestLoadMCPServers(t *testing.T) {
	t.Run("stdio", func(t *testing.T) {
		t.Parallel()
		dir := writeMCPConfig(t, `{
			"mcp_servers": {
				"test": {
					"command": "echo",
					"args": ["hello"],
					"env": {"FOO": "bar"}
				}
			}
		}`)

		servers := LoadAllMCPServers(dir)
		srv, ok := servers["test"]
		if !ok {
			t.Fatal("missing test server")
		}
		if srv.Command != "echo" || len(srv.Args) != 1 || srv.Args[0] != "hello" {
			t.Errorf("got command=%q args=%v", srv.Command, srv.Args)
		}
		if srv.Env["FOO"] != "bar" {
			t.Errorf("env FOO = %q", srv.Env["FOO"])
		}
	})

	t.Run("http", func(t *testing.T) {
		t.Parallel()
		dir := writeMCPConfig(t, `{
			"mcp_servers": {
				"remote": {
					"type": "http",
					"url": "https://mcp.example.com/mcp",
					"headers": {"Authorization": "Bearer test123"}
				}
			}
		}`)

		srv := LoadAllMCPServers(dir)["remote"]
		if srv.Type != "http" {
			t.Errorf("type = %q", srv.Type)
		}
		if srv.URL != "https://mcp.example.com/mcp" {
			t.Errorf("url = %q", srv.URL)
		}
		if srv.Headers["Authorization"] != "Bearer test123" {
			t.Errorf("headers = %v", srv.Headers)
		}
	})
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
		tool := NewMCPTool(c, mt)

		if tool.Name() != "mcp__srv__my-tool" {
			t.Errorf("Name() = %q", tool.Name())
		}
		if tool.Label() != "my-tool" {
			t.Errorf("Label() = %q", tool.Label())
		}
		if tool.Description() != "A test tool" {
			t.Errorf("Description() = %q", tool.Description())
		}
		if tool.Schema()["type"] != "object" {
			t.Errorf("Schema() = %v", tool.Schema())
		}
	})

	t.Run("title_as_label", func(t *testing.T) {
		t.Parallel()
		mt := sdkmcp.Tool{
			Name: "x", Title: "Display Name", Description: "d",
			InputSchema: map[string]any{"type": "object"},
		}
		tool := NewMCPTool(c, mt)
		if tool.Label() != "Display Name" {
			t.Errorf("Label() = %q", tool.Label())
		}
	})

	t.Run("nil_schema_fallback", func(t *testing.T) {
		t.Parallel()
		mt := sdkmcp.Tool{Name: "x", Description: "d"}
		tool := NewMCPTool(c, mt)
		if tool.Schema()["type"] != "object" {
			t.Errorf("Schema() = %v", tool.Schema())
		}
	})

	t.Run("permission_metadata_from_annotations", func(t *testing.T) {
		t.Parallel()
		mt := sdkmcp.Tool{
			Name:        "lookup",
			Description: "Read data from remote source",
			Annotations: &sdkmcp.ToolAnnotation{ReadOnlyHint: true},
		}
		tool := NewMCPTool(c, mt)
		meta := tool.PermissionMetadata()
		if meta.Capability != permission.CapabilityRead {
			t.Fatalf("capability = %q, want %q", meta.Capability, permission.CapabilityRead)
		}
	})

	t.Run("permission_metadata_heuristic_network", func(t *testing.T) {
		t.Parallel()
		mt := sdkmcp.Tool{
			Name:        "web-search",
			Description: "Search the web",
		}
		tool := NewMCPTool(c, mt)
		meta := tool.PermissionMetadata()
		if meta.Capability != permission.CapabilityNetwork {
			t.Fatalf("capability = %q, want %q", meta.Capability, permission.CapabilityNetwork)
		}
	})
}

func TestManagerDirtyFlag(t *testing.T) {
	t.Parallel()

	m := NewManager()
	defer m.Close()

	// Initially not dirty.
	if _, ok := m.RefreshIfDirty(t.Context()); ok {
		t.Error("expected not dirty initially")
	}

	// Simulate a list_changed notification.
	m.dirty.Store(true)

	// First check consumes the flag.
	if _, ok := m.RefreshIfDirty(t.Context()); !ok {
		t.Error("expected dirty after Store(true)")
	}

	// Second check: already consumed.
	if _, ok := m.RefreshIfDirty(t.Context()); ok {
		t.Error("expected not dirty after consume")
	}
}

func TestManagerReconfigureClearsFailuresAndMarksDirty(t *testing.T) {
	t.Parallel()

	m := NewManager()
	defer m.Close()

	m.failures["broken"] = "boom"

	errs := m.Reconfigure(t.Context(), nil)
	if len(errs) != 0 {
		t.Fatalf("expected no reconfigure errors, got %v", errs)
	}
	if len(m.failures) != 0 {
		t.Fatalf("expected failures to be cleared, got %v", m.failures)
	}
	if _, ok := m.RefreshIfDirty(t.Context()); !ok {
		t.Fatal("expected reconfigure to mark manager dirty")
	}
}

// --- helpers ---

func writeMCPConfig(t *testing.T, data string) string {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".codebot")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(data), 0o644)
	return dir
}
