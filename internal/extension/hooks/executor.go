package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"slices"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"

	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/lib/detached"
)

// outcome is the raw result of running a hook's executor.
type outcome struct {
	stdout   []byte
	exitCode int // command: real exit code; prompt/http: 0 success, 1 error
	err      error
}

// executor runs a hook and returns its outcome.
type executor interface {
	execute(ctx context.Context, payload []byte, env []string) outcome
}

// commandExec runs command in a shell with payload on stdin.
type commandExec struct {
	shell   []string // the shell and its options, the command following
	command string
}

// commandFor returns how he's command runs on goos: in sh, but on Windows,
// its command_windows, if it has one, in PowerShell. Windows has no sh of
// its own: a command alone needs one on PATH there, as Git for Windows
// brings.
func commandFor(he config.HookEntry, goos string) (*commandExec, error) {
	sh := &commandExec{shell: []string{"sh", "-c"}, command: he.Command}
	switch {
	case goos != "windows":
		return sh, nil
	case he.CommandWindows != "":
		// powershell.exe comes with Windows; pwsh need not. The user agreed
		// to the hook, so a script of it runs whatever the local execution
		// policy; one set by group policy still holds.
		return &commandExec{shell: []string{"powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command"}, command: he.CommandWindows}, nil
	}
	if _, err := exec.LookPath("sh"); err != nil {
		return nil, errors.New(`no sh on PATH to run it, such as Git for Windows brings; give it a "command_windows" to run in PowerShell`)
	}
	return sh, nil
}

func (c *commandExec) execute(ctx context.Context, payload []byte, env []string) outcome {
	cmd := detached.Command(ctx, c.shell[0], slices.Concat(c.shell[1:], []string{c.command})...)
	cmd.Env = append(cmd.Environ(), env...)
	cmd.Stdin = bytes.NewReader(payload)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return outcome{
			stdout:   stdout.Bytes(),
			exitCode: code,
			err:      fmt.Errorf("command %q: %w (stderr: %s)", c.command, err, stderr.String()),
		}
	}
	return outcome{stdout: stdout.Bytes()}
}

// promptExec sends a single-turn LLM query and returns the response as JSON.
// The prompt template may contain $ARGUMENTS which is replaced with the payload.
type promptExec struct {
	prompt string
	model  func() agentcore.Model
}

func (p *promptExec) execute(ctx context.Context, payload []byte, _ []string) outcome {
	prompt := strings.ReplaceAll(p.prompt, "$ARGUMENTS", string(payload))
	m := p.model()
	req := m.Request
	req.Messages = []litellm.Message{litellm.UserText(prompt)}
	resp, err := m.Client.Chat(ctx, req)
	if err != nil {
		return outcome{exitCode: 1, err: fmt.Errorf("prompt hook: %w", err)}
	}

	text := resp.Text()
	ok, reason := parseHookResponse(text)
	data, _ := json.Marshal(hookOutput{Block: !ok, Reason: reason})
	return outcome{stdout: data}
}

// httpExec sends a POST request with the payload as JSON body.
type httpExec struct {
	url     string
	headers map[string]string
}

func (h *httpExec) execute(ctx context.Context, payload []byte, _ []string) outcome {
	if err := checkSSRF(h.url); err != nil {
		return outcome{exitCode: 1, err: fmt.Errorf("http hook: %w", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(payload))
	if err != nil {
		return outcome{exitCode: 1, err: fmt.Errorf("http hook: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: ssrfGuardedDial,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return outcome{exitCode: 1, err: fmt.Errorf("http hook %s: %w", h.url, err)}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 400 {
		return outcome{stdout: body, exitCode: 1, err: fmt.Errorf("http hook %s: status %d", h.url, resp.StatusCode)}
	}
	return outcome{stdout: body}
}

// checkSSRF validates the URL scheme and performs a pre-flight DNS check
// to block requests targeting private/reserved IP ranges.
func checkSSRF(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("blocked scheme %q (only http/https allowed)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("empty host in URL")
	}

	// Resolve and check all IPs before connecting.
	ips, err := net.DefaultResolver.LookupHost(context.Background(), host)
	if err != nil {
		return fmt.Errorf("DNS lookup failed for %s: %w", host, err)
	}
	for _, ipStr := range ips {
		if isPrivateIP(net.ParseIP(ipStr)) {
			return fmt.Errorf("blocked private/reserved address %s for host %s", ipStr, host)
		}
	}
	return nil
}

// ssrfGuardedDial is a DialContext that rejects connections to private IPs.
// This guards against DNS rebinding (host resolves differently on second lookup).
func ssrfGuardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("http hook: blocked private address %s (DNS rebinding guard)", ipStr)
		}
	}
	// Connect to the first resolved IP to avoid TOCTOU with DNS rebinding.
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
}

// isPrivateIP reports whether ip is in a private or reserved range.
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true // treat unparseable as blocked
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	// IPv4-mapped IPv6 (e.g. ::ffff:127.0.0.1)
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.IsLoopback() || ip4.IsPrivate() || ip4.IsLinkLocalUnicast() || ip4.IsUnspecified()
	}
	return false
}

// parseHookResponse tries to extract {ok, reason} from LLM text output.
// Falls back to ok=true if parsing fails (permissive).
func parseHookResponse(text string) (ok bool, reason string) {
	text = strings.TrimSpace(text)

	// Try JSON first
	var resp struct {
		OK     *bool  `json:"ok"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal([]byte(text), &resp) == nil && resp.OK != nil {
		return *resp.OK, resp.Reason
	}

	// Try to find JSON in the text (LLM may wrap with explanation)
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			if json.Unmarshal([]byte(text[start:end+1]), &resp) == nil && resp.OK != nil {
				return *resp.OK, resp.Reason
			}
		}
	}

	return true, "" // permissive default
}
