package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	mcpclient "github.com/voocel/mcp-sdk-go/client"
	"github.com/voocel/mcp-sdk-go/protocol"
	"github.com/voocel/mcp-sdk-go/transport"
	"github.com/voocel/mcp-sdk-go/transport/streamable"
)

// connectTimeout is the maximum time to wait for an MCP server handshake.
const connectTimeout = 30 * time.Second

// Client wraps a single MCP server connection.
type Client struct {
	name     string
	session  *mcpclient.ClientSession
	cancel   context.CancelFunc // cancels the session-scoped context
	onChange func()             // called when server sends notifications/tools/list_changed
}

// Connect establishes an MCP connection using the transport specified in cfg.
// onChange is called when the server sends a tools/list_changed notification.
func Connect(ctx context.Context, name string, cfg ServerConfig, onChange func()) (*Client, error) {
	var tr transport.Transport
	switch cfg.Type {
	case "http":
		tr = buildHTTPTransport(cfg)
	default: // "stdio" or empty
		tr = buildStdioTransport(cfg)
	}

	c := &Client{name: name, onChange: onChange}

	var opts *mcpclient.ClientOptions
	if onChange != nil {
		opts = &mcpclient.ClientOptions{
			ToolListChangedHandler: func(_ context.Context, _ *protocol.ToolsListChangedNotification) {
				c.onChange()
			},
		}
	}

	cli := mcpclient.NewClient(&mcpclient.ClientInfo{
		Name:    "codebot",
		Version: "1.0.0",
	}, opts)

	// The SDK uses ctx for BOTH the initial handshake AND the lifetime
	// message-handling loop. We create a session-scoped context that only
	// gets cancelled on Close(), and enforce the connect timeout externally.
	sessionCtx, sessionCancel := context.WithCancel(ctx)

	type connectResult struct {
		session *mcpclient.ClientSession
		err     error
	}
	ch := make(chan connectResult, 1)
	go func() {
		s, err := cli.Connect(sessionCtx, tr, nil)
		ch <- connectResult{s, err}
	}()

	timer := time.NewTimer(connectTimeout)
	defer timer.Stop()

	select {
	case <-timer.C:
		sessionCancel()
		return nil, fmt.Errorf("connect to %s: timeout after %v", name, connectTimeout)
	case <-ctx.Done():
		sessionCancel()
		return nil, fmt.Errorf("connect to %s: %w", name, ctx.Err())
	case r := <-ch:
		if r.err != nil {
			sessionCancel()
			return nil, fmt.Errorf("connect to %s: %w", name, r.err)
		}
		c.session = r.session
		c.cancel = sessionCancel
		return c, nil
	}
}

func buildStdioTransport(cfg ServerConfig) *mcpclient.CommandTransport {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		cmd.Env = ExpandEnv(cfg.Env)
	}
	return &mcpclient.CommandTransport{Command: cmd}
}

func buildHTTPTransport(cfg ServerConfig) *streamable.StreamableClientTransport {
	t := &streamable.StreamableClientTransport{
		Endpoint: cfg.URL,
	}
	if len(cfg.Headers) > 0 {
		expanded := ExpandHeaders(cfg.Headers)
		t.HTTPClient = &http.Client{
			Transport: &headerTransport{
				headers: expanded,
				base:    http.DefaultTransport,
			},
		}
	}
	return t
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
func (c *Client) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	result, err := c.session.ListTools(ctx, &protocol.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("list tools from %s: %w", c.name, err)
	}
	return result.Tools, nil
}

// CallTool invokes a tool on the server.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*protocol.CallToolResult, error) {
	return c.session.CallTool(ctx, &protocol.CallToolParams{
		Name:      name,
		Arguments: args,
	})
}

// Instructions returns server instructions from the initialize result, if any.
func (c *Client) Instructions() string {
	if r := c.session.InitializeResult(); r != nil {
		return r.Instructions
	}
	return ""
}

// Close terminates the MCP session and server process.
func (c *Client) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	return c.session.Close()
}
