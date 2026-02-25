package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectTimeout is the maximum time to wait for an MCP server handshake.
const connectTimeout = 30 * time.Second

// Client wraps a single MCP server connection.
type Client struct {
	name     string
	session  *mcp.ClientSession
	onChange func() // called when server sends notifications/tools/list_changed
}

// Connect establishes an MCP connection using the transport specified in cfg.
// onChange is called when the server sends a tools/list_changed notification.
func Connect(ctx context.Context, name string, cfg ServerConfig, onChange func()) (*Client, error) {
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	var transport mcp.Transport
	switch cfg.Type {
	case "http":
		transport = buildHTTPTransport(cfg)
	default: // "stdio" or empty
		transport = buildStdioTransport(cfg)
	}

	c := &Client{name: name, onChange: onChange}

	var opts *mcp.ClientOptions
	if onChange != nil {
		opts = &mcp.ClientOptions{
			ToolListChangedHandler: func(_ context.Context, _ *mcp.ToolListChangedRequest) {
				c.onChange()
			},
		}
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "codebot",
		Version: "1.0.0",
	}, opts)

	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", name, err)
	}

	c.session = session
	return c, nil
}

func buildStdioTransport(cfg ServerConfig) *mcp.CommandTransport {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		cmd.Env = ExpandEnv(cfg.Env)
	}
	return &mcp.CommandTransport{Command: cmd}
}

func buildHTTPTransport(cfg ServerConfig) *mcp.StreamableClientTransport {
	t := &mcp.StreamableClientTransport{
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
func (c *Client) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	result, err := c.session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("list tools from %s: %w", c.name, err)
	}
	return result.Tools, nil
}

// CallTool invokes a tool on the server.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	return c.session.CallTool(ctx, &mcp.CallToolParams{
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
	return c.session.Close()
}
