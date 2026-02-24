package mcp

import (
	"context"
	"fmt"
	"sync"

	"github.com/voocel/agentcore"
)

// Manager manages the lifecycle of multiple MCP server connections.
type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client
}

// NewManager creates an empty Manager.
func NewManager() *Manager {
	return &Manager{clients: make(map[string]*Client)}
}

// StartAll connects to all configured MCP servers in parallel.
// Partial failures are collected; successful servers remain active.
func (m *Manager) StartAll(ctx context.Context, servers map[string]ServerConfig) []error {
	type result struct {
		name   string
		client *Client
		err    error
	}

	ch := make(chan result, len(servers))
	for name, cfg := range servers {
		go func(name string, cfg ServerConfig) {
			c, err := Connect(ctx, name, cfg)
			ch <- result{name: name, client: c, err: err}
		}(name, cfg)
	}

	var errs []error
	for range len(servers) {
		r := <-ch
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.name, r.err))
			continue
		}
		m.mu.Lock()
		m.clients[r.name] = r.client
		m.mu.Unlock()
	}
	return errs
}

// Tools returns all MCP tools from connected servers as agentcore.Tool adapters.
func (m *Manager) Tools(ctx context.Context) []agentcore.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()

	var tools []agentcore.Tool
	for _, c := range m.clients {
		mcpTools, err := c.ListTools(ctx)
		if err != nil {
			continue
		}
		for _, t := range mcpTools {
			tools = append(tools, NewMCPTool(c, t))
		}
	}
	return tools
}

// Instructions collects server instructions from all connected servers.
func (m *Manager) Instructions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []string
	for _, c := range m.clients {
		if inst := c.Instructions(); inst != "" {
			out = append(out, fmt.Sprintf("## %s\n%s", c.Name(), inst))
		}
	}
	return out
}

// Close terminates all MCP server connections.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, c := range m.clients {
		_ = c.Close()
		delete(m.clients, name)
	}
}
