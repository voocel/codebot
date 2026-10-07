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

	"github.com/voocel/mcp-sdk-go/auth"
	mcpclient "github.com/voocel/mcp-sdk-go/client"
	"github.com/voocel/mcp-sdk-go/protocol"
	"github.com/voocel/mcp-sdk-go/transport"
	"github.com/voocel/mcp-sdk-go/transport/stdio"
	"github.com/voocel/mcp-sdk-go/transport/streamhttp"
)

const connectTimeout = 30 * time.Second

type Client struct {
	name         string
	sdk          *mcpclient.Client
	discover     *protocol.DiscoverResult
	cancel       context.CancelFunc
	subscription *mcpclient.Subscription
	onChange     func() // called when server sends notifications/tools/list_changed
	oauth        bool
}

// connect calls onChange on each tools/list_changed notification. authz,
// when not nil, authorizes an HTTP server's requests.
func connect(ctx context.Context, name string, cfg config.MCPServer, authz *auth.Authorizer, onChange func()) (*Client, error) {
	stderr := &tail{}
	tr, err := buildTransport(cfg, authz, stderr)
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
		oauth:    authz != nil,
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

// buildTransport takes cfg as written; any expansion has happened already.
// A stdio server inherits codebot's environment with cfg.Env on top.
func buildTransport(cfg config.MCPServer, authz *auth.Authorizer, stderr *tail) (transport.Transport, error) {
	if cfg.Type == "http" {
		return buildHTTPTransport(cfg, authz), nil
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

// tail keeps the end of a server's stderr to explain failures. Stderr must
// not reach the terminal, where the TUI draws.
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

// said returns the last lines prefixed with a newline, or "" if none.
func (t *tail) said() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	if lines[0] == "" {
		return ""
	}
	return "\n" + strings.Join(lines[max(len(lines)-3, 0):], "\n")
}

func buildHTTPTransport(cfg config.MCPServer, authz *auth.Authorizer) *streamhttp.Transport {
	rt := http.DefaultTransport
	if authz != nil {
		rt = authz.Transport(rt)
	}
	if len(cfg.Headers) > 0 {
		rt = &headerTransport{headers: cfg.Headers, base: rt}
	}
	return streamhttp.New(cfg.URL, &streamhttp.TransportOptions{HTTPClient: &http.Client{Transport: rt}})
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

func (c *Client) Name() string { return c.name }

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

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*protocol.CallToolResult, error) {
	return c.sdk.CallTool(ctx, &protocol.CallToolParams{
		Name:      name,
		Arguments: args,
	})
}

func (c *Client) Instructions() string {
	return c.discover.Instructions
}

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
