package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

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

// Connect establishes an MCP connection using the transport specified in cfg.
// onChange is called when the server sends a tools/list_changed notification.
func Connect(ctx context.Context, name string, cfg ServerConfig, onChange func()) (*Client, error) {
	tr, err := buildTransport(cfg)
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
		return nil, errors.Join(fmt.Errorf("connect to %s: %w", name, err), c.Close())
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

func buildTransport(cfg ServerConfig) (transport.Transport, error) {
	if cfg.Type == "http" {
		return buildHTTPTransport(cfg), nil
	}
	return buildStdioTransport(cfg)
}

func buildStdioTransport(cfg ServerConfig) (*stdio.Command, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		cmd.Env = ExpandEnv(cfg.Env)
	}
	return stdio.NewCommand(cmd, nil)
}

func buildHTTPTransport(cfg ServerConfig) *streamhttp.Transport {
	var opts *streamhttp.TransportOptions
	if len(cfg.Headers) > 0 {
		expanded := ExpandHeaders(cfg.Headers)
		opts = &streamhttp.TransportOptions{
			HTTPClient: &http.Client{Transport: &headerTransport{
				headers: expanded,
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
		fmt.Fprintf(os.Stderr, "warning: MCP tool-change subscription %s ended: %v\n", c.name, err)
		return
	}
	fmt.Fprintf(os.Stderr, "warning: MCP tool-change subscription %s ended\n", c.name)
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
