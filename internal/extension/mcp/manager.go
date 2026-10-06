package mcp

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"

	"github.com/voocel/agentcore"

	"github.com/voocel/codebot/internal/agent/permission"
	"github.com/voocel/codebot/internal/infra/config"
)

// Manager manages the lifecycle of multiple MCP server connections.
type Manager struct {
	mu       sync.Mutex
	clients  map[string]*Client
	configs  map[string]config.MCPServer // what each client connected with
	failures map[string]string           // server name → error message
	onChange func()                      // a server signalled tools/list_changed
}

// NewManager creates an empty Manager. onChange is called, on a goroutine of
// the server's connection, whenever a server's tool list changes.
func NewManager(onChange func()) *Manager {
	return &Manager{
		clients:  make(map[string]*Client),
		configs:  make(map[string]config.MCPServer),
		failures: make(map[string]string),
		onChange: onChange,
	}
}

// Configure makes the connected servers those of servers. It disconnects
// those gone or changed, then connects, in parallel, the new, the changed
// and those that failed before; a server connected as configured stays
// connected. It returns the servers that failed to connect. Calls must not
// overlap: the caller orders them.
func (m *Manager) Configure(ctx context.Context, servers map[string]config.MCPServer) []error {
	m.mu.Lock()
	var stale []*Client
	for name, c := range m.clients {
		if cfg, ok := servers[name]; !ok || !reflect.DeepEqual(cfg, m.configs[name]) {
			stale = append(stale, c)
			delete(m.clients, name)
			delete(m.configs, name)
		}
	}
	clear(m.failures)
	pending := map[string]config.MCPServer{}
	for name, cfg := range servers {
		if _, ok := m.clients[name]; !ok {
			pending[name] = cfg
		}
	}
	m.mu.Unlock()
	for _, c := range stale {
		_ = c.Close()
	}

	type result struct {
		name   string
		client *Client
		err    error
	}
	ch := make(chan result, len(pending))
	for name, cfg := range pending {
		go func() {
			c, err := connect(ctx, name, cfg, m.onChange)
			ch <- result{name, c, err}
		}()
	}
	var errs []error
	for range len(pending) {
		r := <-ch
		m.mu.Lock()
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.name, r.err))
			m.failures[r.name] = r.err.Error()
		} else {
			m.clients[r.name], m.configs[r.name] = r.client, pending[r.name]
		}
		m.mu.Unlock()
	}
	return errs
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
