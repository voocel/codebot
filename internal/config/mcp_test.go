package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSettings(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A project server replaces the global one of the same name; the rest merge.
func TestMCPServersMergeByName(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, filepath.Join(home, ConfigDir, "settings.json"), `{"mcp_servers": {
		"docs": {"command": "global-docs"},
		"search": {"type": "http", "url": "https://mcp.example.com/mcp", "headers": {"Authorization": "Bearer ${TOKEN}"}}}}`)
	writeSettings(t, SettingsPath(cwd), `{"mcp_servers": {"docs": {"command": "project-docs", "args": ["-v"]}}}`)

	r, err := LoadSettingsStrict(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.MCPServers["docs"]; got.Command != "project-docs" || len(got.Args) != 1 {
		t.Fatalf("docs = %+v, want the project's", got)
	}
	if got := r.MCPServers["search"]; got.Type != "http" || got.Headers["Authorization"] != "Bearer ${TOKEN}" {
		t.Fatalf("search = %+v", got)
	}
}

// Saving a setting, as a model switch does, keeps the MCP servers.
func TestPatchKeepsMCPServers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ConfigDir, "settings.json")
	writeSettings(t, path, `{"model": "a", "mcp_servers": {"docs": {"command": "docs-server"}}}`)

	model := "b"
	if err := patchSettingsFile(globalSettingsPath(), Settings{Model: &model}); err != nil {
		t.Fatal(err)
	}
	saved, err := loadSettingsFileStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if *saved.Model != "b" || saved.MCPServers["docs"].Command != "docs-server" {
		t.Fatalf("saved = model %q, servers %+v", *saved.Model, saved.MCPServers)
	}
}
