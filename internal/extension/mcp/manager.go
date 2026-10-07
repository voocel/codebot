package mcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"

	"github.com/voocel/agentcore"
	"github.com/voocel/mcp-sdk-go/auth"

	"github.com/voocel/codebot/internal/agent/permission"
	"github.com/voocel/codebot/internal/infra/config"
)

type Manager struct {
	mu       sync.Mutex
	clients  map[string]*Client
	configs  map[string]config.MCPServer // what each client connected with
	servers  map[string]config.MCPServer // the last configuration
	failures map[string]error
	// authz outlive connections, so a login sees the challenge of the
	// connection that failed.
	authz    map[auth.Config]*auth.Authorizer
	store    auth.Store
	onChange func() // a server signalled tools/list_changed
}

// NewManager calls onChange, on the connection's goroutine, whenever a
// server's tool list changes.
func NewManager(onChange func()) *Manager {
	return &Manager{
		clients:  make(map[string]*Client),
		configs:  make(map[string]config.MCPServer),
		failures: make(map[string]error),
		authz:    make(map[auth.Config]*auth.Authorizer),
		store:    tokenStore{path: oauthPath()},
		onChange: onChange,
	}
}

// Failure is a server that did not connect.
type Failure struct {
	Server string
	Err    error
	Login  bool // it wants an OAuth login
}

// Configure connects only servers that are new, changed or failed before;
// connected servers whose config is unchanged stay up. Calls must not
// overlap; the caller serializes them.
func (m *Manager) Configure(ctx context.Context, servers map[string]config.MCPServer) []Failure {
	m.mu.Lock()
	m.servers = servers
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
		authz := m.authorizer(cfg)
		go func() {
			c, err := connect(ctx, name, cfg, authz, m.onChange)
			ch <- result{name, c, err}
		}()
	}
	var failures []Failure
	for range len(pending) {
		r := <-ch
		m.mu.Lock()
		if r.err != nil {
			m.failures[r.name] = r.err
			failures = append(failures, m.failure(r.name, r.err))
		} else {
			m.clients[r.name], m.configs[r.name] = r.client, pending[r.name]
		}
		m.mu.Unlock()
	}
	return failures
}

// Login starts an OAuth login to the server name; the caller completes it
// with Wait and connects again.
func (m *Manager) Login(ctx context.Context, name string) (*auth.Login, error) {
	authz, err := m.oauthOf(name)
	if err != nil {
		return nil, err
	}
	l, err := authz.Login(ctx)
	if errors.Is(err, auth.ErrClientRequired) {
		return nil, fmt.Errorf("%w: register an OAuth app there with callback URL http://127.0.0.1/callback, then set its client_id and client_secret as mcp_servers.%s.oauth", err, name)
	}
	return l, err
}

// Logout forgets the server's token and disconnects it, so the next
// Configure connects it again without one.
func (m *Manager) Logout(name string) error {
	authz, err := m.oauthOf(name)
	if err != nil {
		return err
	}
	if err := authz.Logout(); err != nil {
		return err
	}
	m.mu.Lock()
	c := m.clients[name]
	delete(m.clients, name)
	delete(m.configs, name)
	m.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	return nil
}

func (m *Manager) oauthOf(name string) (*auth.Authorizer, error) {
	m.mu.Lock()
	cfg, ok := m.servers[name]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no MCP server is called %q", name)
	}
	authz := m.authorizer(cfg)
	if authz == nil {
		return nil, fmt.Errorf("%s does not log in with OAuth: only HTTP servers without an Authorization header do", name)
	}
	return authz, nil
}

// clientMetadataURL is codebot's Client ID Metadata Document, served from
// site/oauth/client.json.
const clientMetadataURL = "https://voocel.github.io/codebot/oauth/client.json"

// authorizer returns the server's OAuth authorizer, or nil when it does not
// use OAuth. Caller does not hold m.mu.
func (m *Manager) authorizer(cfg config.MCPServer) *auth.Authorizer {
	if !usesOAuth(cfg) {
		return nil
	}
	ac := auth.Config{Server: cfg.URL, Store: m.store, ClientMetadataURL: clientMetadataURL}
	if cfg.OAuth != nil {
		ac.ClientID, ac.ClientSecret = cfg.OAuth.ClientID, cfg.OAuth.ClientSecret
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.authz[ac]
	if a == nil {
		a = auth.New(ac)
		m.authz[ac] = a
	}
	return a
}

// failure describes the server name's connection error. Caller holds m.mu.
func (m *Manager) failure(name string, err error) Failure {
	return Failure{Server: name, Err: err, Login: usesOAuth(m.servers[name]) && needsLogin(err)}
}

// sortedClients keeps tools and instructions in a stable order across
// refreshes; map order would change the request prefix and break the prompt
// cache. Caller holds m.mu.
func (m *Manager) sortedClients() []*Client {
	names := slices.Sorted(maps.Keys(m.clients))
	out := make([]*Client, 0, len(names))
	for _, name := range names {
		out = append(out, m.clients[name])
	}
	return out
}

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

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, c := range m.clients {
		_ = c.Close()
		delete(m.clients, name)
	}
}

type ServerStatus struct {
	Name      string
	ToolCount int
	Error     string // non-empty if connection failed
	Login     bool   // the server wants an OAuth login
	ListError string // non-empty if connected but ListTools failed
}

// Status includes failed servers. Tools are listed in parallel without
// holding the lock.
func (m *Manager) Status(ctx context.Context) []ServerStatus {
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	failures := make([]Failure, 0, len(m.failures))
	for name, err := range m.failures {
		failures = append(failures, m.failure(name, err))
	}
	m.mu.Unlock()

	type result struct {
		name  string
		count int
		err   error
		login bool
	}
	ch := make(chan result, len(clients))
	for _, c := range clients {
		go func(c *Client) {
			tools, err := c.ListTools(ctx)
			ch <- result{name: c.Name(), count: len(tools), err: err, login: c.oauth && needsLogin(err)}
		}(c)
	}

	out := make([]ServerStatus, 0, len(clients)+len(failures))
	for range clients {
		r := <-ch
		s := ServerStatus{Name: r.name, ToolCount: r.count, Login: r.login}
		if r.err != nil {
			s.ListError = r.err.Error()
		}
		out = append(out, s)
	}
	for _, f := range failures {
		out = append(out, ServerStatus{Name: f.Server, Error: f.Err.Error(), Login: f.Login})
	}
	return out
}
