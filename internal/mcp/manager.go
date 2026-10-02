package mcp

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/voocel/agentcore"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/permission"
)

// Manager manages the lifecycle of multiple MCP server connections.
type Manager struct {
	mu       sync.Mutex
	clients  map[string]*Client
	failures map[string]string // server name → error message
	onChange func()            // a server signalled tools/list_changed
}

// NewManager creates an empty Manager. onChange is called, on a goroutine of
// the server's connection, whenever a server's tool list changes.
func NewManager(onChange func()) *Manager {
	return &Manager{
		clients:  make(map[string]*Client),
		failures: make(map[string]string),
		onChange: onChange,
	}
}

// StartAll connects to all configured MCP servers in parallel.
// Partial failures are collected; successful servers remain active.
func (m *Manager) StartAll(ctx context.Context, servers map[string]config.MCPServer) []error {
	type result struct {
		name   string
		client *Client
		err    error
	}

	ch := make(chan result, len(servers))
	for name, cfg := range servers {
		go func(name string, cfg config.MCPServer) {
			c, err := connect(ctx, name, cfg, m.onChange)
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

// Reconfigure replaces the active MCP server set with a new configuration.
// Existing clients are closed and connection failures are reset.
func (m *Manager) Reconfigure(ctx context.Context, servers map[string]config.MCPServer) []error {
	oldClients := m.reset()
	for _, c := range oldClients {
		_ = c.Close()
	}

	if len(servers) == 0 {
		return nil
	}
	return m.StartAll(ctx, servers)
}

// sortedClients returns connected clients in deterministic (server name)
// order. Tools and instructions feed the LLM request's tools array and the
// system prompt's mcp overlay; iterating the map directly would shuffle
// their bytes across refreshes and bust the provider prompt cache from the
// prefix onward. Caller must hold m.mu.
func (m *Manager) sortedClients() []*Client {
	names := slices.Sorted(maps.Keys(m.clients))
	out := make([]*Client, 0, len(names))
	for _, name := range names {
		out = append(out, m.clients[name])
	}
	return out
}

// Tools returns the tools of the connected servers, and how the permission
// engine sees each, by name.
func (m *Manager) Tools(ctx context.Context) ([]agentcore.Tool, map[string]permission.Metadata) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var tools []agentcore.Tool
	perms := map[string]permission.Metadata{}
	for _, c := range m.sortedClients() {
		listed, err := c.ListTools(ctx)
		if err != nil {
			continue
		}
		for _, t := range listed {
			tool := newTool(c, t)
			tools = append(tools, tool)
			perms[tool.Name] = permissionOf(t)
		}
	}
	return tools, perms
}

// Instructions collects server instructions from all connected servers.
func (m *Manager) Instructions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []string
	for _, c := range m.sortedClients() {
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

// Status returns the status of all MCP servers, including failed ones. The
// servers list their tools in parallel, under ctx, without holding the lock.
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

func (m *Manager) reset() []*Client {
	m.mu.Lock()
	defer m.mu.Unlock()

	oldClients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		oldClients = append(oldClients, c)
	}
	m.clients = make(map[string]*Client)
	m.failures = make(map[string]string)
	return oldClients
}
