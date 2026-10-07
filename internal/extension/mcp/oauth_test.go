package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/mcp-sdk-go/auth"
	sdkmcp "github.com/voocel/mcp-sdk-go/protocol"
	sdkserver "github.com/voocel/mcp-sdk-go/server"
	sdkhttp "github.com/voocel/mcp-sdk-go/transport/streamhttp"

	"github.com/voocel/codebot/internal/infra/config"
)

// protectedServer is an MCP server behind a minimal authorization server
// that takes only the client codebot-test, registered beforehand; revoke
// invalidates every token it issued.
func protectedServer(t *testing.T) (endpoint string, revoke func()) {
	var (
		mu        sync.Mutex
		challenge = map[string]string{} // code → PKCE challenge
		valid     = map[string]bool{}   // access tokens
		n         int
	)
	var as, rs *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           as.URL,
			"authorization_endpoint":           as.URL + "/authorize",
			"token_endpoint":                   as.URL + "/token",
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		n++
		code := fmt.Sprint("code-", n)
		challenge[code] = q.Get("code_challenge")
		mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		id, secret, _ := r.BasicAuth()
		mu.Lock()
		defer mu.Unlock()
		if id != "codebot-test" || secret != "secret" || challenge[r.Form.Get("code")] != base64.RawURLEncoding.EncodeToString(sum[:]) {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		n++
		token := fmt.Sprint("access-", n)
		valid[token] = true
		json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 3600})
	})
	as = httptest.NewServer(mux)
	t.Cleanup(as.Close)

	backend := sdkserver.New(&sdkserver.Options{Impl: sdkmcp.Implementation{Name: "protected", Version: "1"}})
	sdkserver.AddTool(backend, &sdkmcp.Tool{Name: "echo"}, func(ctx context.Context, req *sdkserver.CallRequest, in struct{}) (sdkmcp.ToolResponse, string, error) {
		return nil, "ok", nil
	})
	mcp := sdkhttp.NewHandler(backend, nil)
	rmux := http.NewServeMux()
	rmux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": rs.URL + "/mcp", "authorization_servers": []string{as.URL}})
	})
	rmux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		ok := valid[token]
		mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+rs.URL+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mcp.ServeHTTP(w, r)
	})
	rs = httptest.NewServer(rmux)
	t.Cleanup(rs.Close)
	return rs.URL + "/mcp", func() {
		mu.Lock()
		clear(valid)
		mu.Unlock()
	}
}

func TestLoginToAnOAuthServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := NewManager(nil)
	defer m.Close()
	endpoint, revoke := protectedServer(t)
	servers := map[string]config.MCPServer{"linear": {Type: "http", URL: endpoint, OAuth: &config.MCPOAuth{ClientID: "codebot-test", ClientSecret: "secret"}}}

	failures := m.Configure(t.Context(), servers)
	if len(failures) != 1 || !failures[0].Login {
		t.Fatalf("before login: %+v", failures)
	}
	if st := m.Status(t.Context()); len(st) != 1 || !st[0].Login {
		t.Fatalf("status before login: %+v", st)
	}

	l, err := m.Login(t.Context(), "linear")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		resp, err := http.Get(l.URL) // the user agrees in the browser
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	if err := l.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if failures := m.Configure(t.Context(), servers); len(failures) != 0 {
		t.Fatalf("after login: %+v", failures)
	}
	info, err := os.Stat(oauthPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", info, err)
	}

	// A connected server whose token the server stops taking.
	revoke()
	if st := m.Status(t.Context()); len(st) != 1 || !st[0].Login {
		t.Fatalf("status after revocation: %+v", st)
	}

	if err := m.Logout("linear"); err != nil {
		t.Fatal(err)
	}
	if failures := m.Configure(t.Context(), servers); len(failures) != 1 || !failures[0].Login {
		t.Fatalf("after logout: %+v", failures)
	}
}

func TestOnlyOAuthServersLogIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := NewManager(nil)
	defer m.Close()
	endpoint, _ := protectedServer(t)
	failures := m.Configure(t.Context(), map[string]config.MCPServer{
		"static": {Type: "http", URL: endpoint, Headers: map[string]string{"authorization": "Bearer mine"}},
		"local":  {Command: "false"},
	})
	for _, f := range failures {
		if f.Login {
			t.Errorf("%s wants a login", f.Server)
		}
	}
	for _, name := range []string{"static", "local", "missing"} {
		if _, err := m.Login(t.Context(), name); err == nil {
			t.Errorf("%s: login started", name)
		}
	}
}

func TestAServerWithoutClientDocumentsNeedsARegisteredClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := NewManager(nil)
	defer m.Close()
	endpoint, _ := protectedServer(t)
	m.Configure(t.Context(), map[string]config.MCPServer{"github": {Type: "http", URL: endpoint}})
	_, err := m.Login(t.Context(), "github")
	if !errors.Is(err, auth.ErrClientRequired) || !strings.Contains(err.Error(), "mcp_servers.github.oauth") {
		t.Fatalf("got %v", err)
	}
}
