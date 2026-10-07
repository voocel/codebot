package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/lib/regular"
)

// server is an mcp.json entry.
type server struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// readMCP drops every server if mcp.json breaks the format, and only the
// broken server otherwise.
func (p *Plugin) readMCP() (problems []error) {
	path := filepath.Join(p.Root, "mcp.json")
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	file, err := p.inside(path)
	if err != nil {
		return []error{err}
	}
	data, err := regular.ReadFile(file)
	if err != nil {
		return []error{err}
	}
	var f struct {
		Schema     string                     `json:"$schema"`
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	switch err := dec.Decode(&f); {
	case err != nil:
		return []error{fmt.Errorf("%s: %w", path, err)}
	case f.Schema != mcpSchema:
		return []error{fmt.Errorf("%s: $schema must be %s", path, mcpSchema)}
	case f.MCPServers == nil:
		return []error{fmt.Errorf("%s: no mcpServers", path)}
	}
	p.MCP = map[string]config.MCPServer{}
	for _, name := range slices.Sorted(maps.Keys(f.MCPServers)) {
		srv, err := p.decodeServer(f.MCPServers[name])
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: server %q: %w", path, name, err))
			continue
		}
		p.MCP[name] = srv
	}
	return problems
}

func (p *Plugin) decodeServer(raw json.RawMessage) (config.MCPServer, error) {
	var s server
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return config.MCPServer{}, err
	}
	return p.mcpServer(s)
}

// mcpServer expands only ${PLUGIN_ROOT} and ${PLUGIN_DATA}, and sets both in
// the server's environment.
func (p *Plugin) mcpServer(s server) (config.MCPServer, error) {
	switch s.Type {
	case "stdio":
		if s.URL != "" || s.Headers != nil {
			return config.MCPServer{}, errors.New("a stdio server takes no url or headers")
		}
	case "streamable-http":
		if s.Command != "" || s.Args != nil || s.Env != nil || s.Cwd != "" {
			return config.MCPServer{}, errors.New("a streamable-http server takes no command, args, env or cwd")
		}
		if err := checkURL(s.URL); err != nil {
			return config.MCPServer{}, err
		}
		if err := checkHeaders(s.Headers); err != nil {
			return config.MCPServer{}, err
		}
		return config.MCPServer{Type: "http", URL: s.URL, Headers: s.Headers}, nil
	case "sse":
		return config.MCPServer{}, errors.New("codebot does not support sse servers")
	default:
		return config.MCPServer{}, fmt.Errorf("unknown type %q", s.Type)
	}

	expand := strings.NewReplacer("${PLUGIN_ROOT}", p.Root, "${PLUGIN_DATA}", p.Data).Replace
	command, err := p.command(s.Command)
	if err != nil {
		return config.MCPServer{}, err
	}
	cwd, err := p.cwd(s.Cwd)
	if err != nil {
		return config.MCPServer{}, err
	}
	srv := config.MCPServer{Type: "stdio", Command: command, Cwd: cwd, Env: map[string]string{}}
	for _, a := range s.Args {
		srv.Args = append(srv.Args, expand(a))
	}
	for k, v := range s.Env {
		if k == "PLUGIN_ROOT" || k == "PLUGIN_DATA" {
			return config.MCPServer{}, fmt.Errorf("env may not set %s", k)
		}
		srv.Env[k] = expand(v)
	}
	srv.Env["PLUGIN_ROOT"], srv.Env["PLUGIN_DATA"] = p.Root, p.Data
	return srv, nil
}

func (p *Plugin) command(c string) (string, error) {
	if rel, ok := strings.CutPrefix(c, "./"); ok {
		return p.inside(filepath.Join(p.Root, filepath.FromSlash(rel)))
	}
	if c == "" || strings.ContainsAny(c, "/\\ \t\n") {
		return "", fmt.Errorf("command %q is neither an executable's name nor a path starting ./", c)
	}
	return c, nil
}

func (p *Plugin) cwd(c string) (string, error) {
	for prefix, dir := range map[string]string{"./": p.Root, "${PLUGIN_ROOT}": p.Root, "${PLUGIN_DATA}": p.Data} {
		rest, ok := strings.CutPrefix(c, prefix)
		if !ok || prefix != "./" && rest != "" && !strings.HasPrefix(rest, "/") {
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(rest))
		if !within(dir, path) {
			return "", fmt.Errorf("cwd %q leads outside %s", c, dir)
		}
		if dir == p.Root {
			return p.inside(path)
		}
		return path, nil
	}
	if c == "" {
		return p.Root, nil
	}
	return "", fmt.Errorf("cwd %q is not in the plugin or its data", c)
}

func checkURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return err
	case u.Scheme != "https" && u.Scheme != "http", u.Host == "":
		return fmt.Errorf("url %q is not an absolute http(s) URL", raw)
	case u.User != nil || u.Fragment != "":
		return fmt.Errorf("url %q may not hold a user or a fragment", raw)
	case u.Scheme == "http" && !loopback(u.Hostname()):
		return fmt.Errorf("url %q must be https", raw)
	}
	return nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func checkHeaders(h map[string]string) error {
	seen := map[string]bool{}
	for k := range h {
		if seen[strings.ToLower(k)] {
			return fmt.Errorf("header %q is given twice", k)
		}
		seen[strings.ToLower(k)] = true
	}
	return nil
}
