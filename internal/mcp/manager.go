package mcp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/voocel/agentcore"
)

// Manager manages the lifecycle of multiple MCP server connections.
type Manager struct {
	mu       sync.Mutex
	clients  map[string]*Client
	failures map[string]string // server name → error message
	dirty    atomic.Bool       // set when any server signals tools/list_changed
}

// NewManager creates an empty Manager.
func NewManager() *Manager {
	return &Manager{
		clients:  make(map[string]*Client),
		failures: make(map[string]string),
	}
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
			c, err := Connect(ctx, name, cfg, func() { m.dirty.Store(true) })
			ch <- result{name: name, client: c, err: err}
		}(name, cfg)
	}

	var errs []error
	for range len(servers) {
		r := <-ch
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.name, r.err))
			m.mu.Lock()
			m.failures[r.name] = r.err.Error()
			m.mu.Unlock()
			continue
		}
		m.mu.Lock()
		m.clients[r.name] = r.client
		m.mu.Unlock()
	}
	return errs
}

// MarkDirty forces the dirty flag so that the next RefreshIfDirty call
// triggers a tool reload. Used after initial async connection completes.
func (m *Manager) MarkDirty() { m.dirty.Store(true) }

// RefreshIfDirty re-fetches tools from all servers if any sent a list_changed
// notification since the last call. Returns the new tool list and true,
// or nil and false if nothing changed. Safe to call from the main loop.
func (m *Manager) RefreshIfDirty(ctx context.Context) ([]agentcore.Tool, bool) {
	if !m.dirty.CompareAndSwap(true, false) {
		return nil, false
	}
	return m.Tools(ctx), true
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

// ServerStatus describes a connected or failed MCP server.
type ServerStatus struct {
	Name      string
	ToolCount int
	Error     string // non-empty if connection failed
	ListError string // non-empty if connected but ListTools failed
}

// Status returns the status of all MCP servers, including failed ones.
// ListTools calls run in parallel with per-server timeout; the mutex is not
// held during network I/O.
func (m *Manager) Status(ctx context.Context) []ServerStatus {
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	failures := make(map[string]string, len(m.failures))
	for k, v := range m.failures {
		failures[k] = v
	}
	m.mu.Unlock()

	type result struct {
		name  string
		count int
		err   error
	}
	ch := make(chan result, len(clients))
	for _, c := range clients {
		go func(c *Client) {
			tools, err := c.ListTools(ctx)
			ch <- result{name: c.Name(), count: len(tools), err: err}
		}(c)
	}

	out := make([]ServerStatus, 0, len(clients)+len(failures))
	for range clients {
		r := <-ch
		s := ServerStatus{Name: r.name, ToolCount: r.count}
		if r.err != nil {
			s.ListError = r.err.Error()
		}
		out = append(out, s)
	}
	for name, errMsg := range failures {
		out = append(out, ServerStatus{Name: name, Error: errMsg})
	}
	return out
}
