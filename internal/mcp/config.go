package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// ServerConfig describes a single MCP server connection.
//
// Stdio example (default):
//
//	{"command": "npx", "args": ["-y", "@upstash/context7-mcp"], "env": {"KEY": "${VAR}"}}
//
// HTTP example:
//
//	{"type": "http", "url": "https://mcp.example.com/mcp", "headers": {"Authorization": "Bearer ${TOKEN}"}}
type ServerConfig struct {
	Type    string            `json:"type,omitempty"` // "stdio" (default) or "http"
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// LoadAllMCPServers merges MCP server configs from global (~/.codebot/settings.json)
// and project (<cwd>/.codebot/settings.json). Project-level overrides global.
func LoadAllMCPServers(cwd string) map[string]ServerConfig {
	home, _ := os.UserHomeDir()

	merged := make(map[string]ServerConfig)

	// Global scope first (lower priority).
	if home != "" {
		for k, v := range loadMCPServersFrom(filepath.Join(home, ".codebot", "settings.json")) {
			merged[k] = v
		}
	}

	// Project scope overrides global.
	for k, v := range loadMCPServersFrom(filepath.Join(cwd, ".codebot", "settings.json")) {
		merged[k] = v
	}

	if len(merged) == 0 {
		return nil
	}
	return merged
}

func loadMCPServersFrom(path string) map[string]ServerConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw struct {
		MCPServers map[string]ServerConfig `json:"mcp_servers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		fmt.Fprintf(os.Stderr, "warning: malformed mcp_servers in %s: %v\n", path, err)
		return nil
	}
	return raw.MCPServers
}

var envVarRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// expandEnvStr expands ${VAR} references in a string from OS environment.
func expandEnvStr(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		name := envVarRe.FindStringSubmatch(match)[1]
		return os.Getenv(name)
	})
}

// ExpandEnv converts a map of env vars to KEY=VALUE format,
// expanding ${VAR} references from the OS environment.
// The result is appended to the current process environment.
func ExpandEnv(env map[string]string) []string {
	base := os.Environ()
	for k, v := range env {
		base = append(base, k+"="+expandEnvStr(v))
	}
	return base
}

// ExpandHeaders expands ${VAR} in header values.
func ExpandHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = expandEnvStr(v)
	}
	return out
}
