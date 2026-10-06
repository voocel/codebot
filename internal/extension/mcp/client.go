package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/voocel/codebot/internal/infra/config"

	mcpclient "github.com/voocel/mcp-sdk-go/client"
	"github.com/voocel/mcp-sdk-go/protocol"
	"github.com/voocel/mcp-sdk-go/transport"
	"github.com/voocel/mcp-sdk-go/transport/stdio"
	"github.com/voocel/mcp-sdk-go/transport/streamhttp"
)

// connectTimeout is the maximum time to wait for an MCP server handshake.
const connectTimeout = 30 * time.Second

// Client wraps a single MCP server connection.
type Client struct {
	name         string
	sdk          *mcpclient.Client
	discover     *protocol.DiscoverResult
	cancel       context.CancelFunc
	subscription *mcpclient.Subscription
	onChange     func() // called when server sends notifications/tools/list_changed
}

// connect establishes an MCP connection using the transport specified in cfg.
// onChange is called when the server sends a tools/list_changed notification.
func connect(ctx context.Context, name string, cfg config.MCPServer, onChange func()) (*Client, error) {
	stderr := &tail{}
	tr, err := buildTransport(cfg, stderr)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", name, err)
	}

	sdk := mcpclient.New(tr, &mcpclient.Options{
		Info: &protocol.Implementation{Name: "codebot", Version: "1.0.0"},
	})
	lifetimeCtx, lifetimeCancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &Client{
		name:     name,
		sdk:      sdk,
		cancel:   lifetimeCancel,
		onChange: onChange,
	}

	connectCtx, connectCancel := context.WithTimeout(ctx, connectTimeout)
	defer connectCancel()

	discover, err := sdk.Discover(connectCtx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("connect to %s: %w%s", name, err, stderr.said()), c.Close())
	}
	c.discover = discover

	tools := discover.Capabilities.Tools
	if onChange == nil || tools == nil || !tools.ListChanged {
		return c, nil
	}

	type listenResult struct {
		sub *mcpclient.Subscription
		err error
	}
	ready := make(chan listenResult, 1)
	go func() {
		sub, listenErr := sdk.Listen(lifetimeCtx, protocol.SubscriptionFilter{ToolsListChanged: true})
		ready <- listenResult{sub: sub, err: listenErr}
	}()

	select {
	case <-connectCtx.Done():
		return nil, errors.Join(fmt.Errorf("connect to %s: %w", name, connectCtx.Err()), c.Close())
	case result := <-ready:
		if result.err != nil {
			return nil, errors.Join(
				fmt.Errorf("subscribe to tool changes from %s: %w", name, result.err),
				c.Close(),
			)
		}
		if !result.sub.Ack().ToolsListChanged {
			return nil, errors.Join(
				fmt.Errorf("subscribe to tool changes from %s: server did not acknowledge toolsListChanged", name),
				result.sub.Close(),
				c.Close(),
			)
		}
		c.subscription = result.sub
		go c.watchToolChanges(lifetimeCtx)
		return c, nil
	}
}

// buildTransport returns the transport cfg configures, taken as written:
// whatever cfg refers to is expanded already. A server it runs inherits
// codebot's environment, cfg.Env over it, and writes its stderr to stderr.
func buildTransport(cfg config.MCPServer, stderr *tail) (transport.Transport, error) {
	if cfg.Type == "http" {
		return buildHTTPTransport(cfg), nil
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Cwd
	if len(cfg.Env) > 0 {
		cmd.Env = os.Environ()
		for _, k := range slices.Sorted(maps.Keys(cfg.Env)) {
			cmd.Env = append(cmd.Env, k+"="+cfg.Env[k])
		}
	}
	return stdio.NewCommand(cmd, &stdio.CommandOptions{Stderr: stderr})
}

// tail keeps the end of what a server writes to its stderr, which says why
// it failed. The terminal never sees it: a TUI draws there.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailSize = 2048

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - tailSize; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

// said returns the last lines written, after a newline; "" for none.
func (t *tail) said() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	if lines[0] == "" {
		return ""
	}
	return "\n" + strings.Join(lines[max(len(lines)-3, 0):], "\n")
}

func buildHTTPTransport(cfg config.MCPServer) *streamhttp.Transport {
	var opts *streamhttp.TransportOptions
	if len(cfg.Headers) > 0 {
		opts = &streamhttp.TransportOptions{
			HTTPClient: &http.Client{Transport: &headerTransport{
				headers: cfg.Headers,
				base:    http.DefaultTransport,
			}},
		}
	}
	return streamhttp.New(cfg.URL, opts)
}

func (c *Client) watchToolChanges(ctx context.Context) {
	for event := range c.subscription.Events() {
		if event.Method == protocol.NotificationToolsListChanged {
			c.onChange()
		}
	}
	if ctx.Err() != nil {
		return
	}
	if err := c.subscription.Err(); err != nil {
		log.Printf("mcp: the tool-change subscription of %s ended: %v", c.name, err)
		return
	}
	log.Printf("mcp: the tool-change subscription of %s ended", c.name)
}

// headerTransport injects custom headers into every HTTP request.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.base.RoundTrip(req)
}

// Name returns the server name.
func (c *Client) Name() string { return c.name }

// ListTools fetches the tool list from the server.
func (c *Client) ListTools(ctx context.Context) ([]*protocol.Tool, error) {
	if c.discover.Capabilities.Tools == nil {
		return nil, nil
	}
	var tools []*protocol.Tool
	for tool, err := range c.sdk.Tools(ctx) {
		if err != nil {
			return nil, fmt.Errorf("list tools from %s: %w", c.name, err)
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// CallTool invokes a tool on the server.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*protocol.CallToolResult, error) {
	return c.sdk.CallTool(ctx, &protocol.CallToolParams{
		Name:      name,
		Arguments: args,
	})
}

// Instructions returns server instructions from the discovery result, if any.
func (c *Client) Instructions() string {
	return c.discover.Instructions
}

// Close terminates the MCP subscription and underlying transport.
func (c *Client) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	var errs []error
	if c.subscription != nil {
		errs = append(errs, c.subscription.Close())
	}
	if c.sdk != nil {
		errs = append(errs, c.sdk.Close())
	}
	return errors.Join(errs...)
}
