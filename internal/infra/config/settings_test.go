package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestMergeSettingsProviderAPI(t *testing.T) {
	base := Settings{
		Providers: map[string]*ProviderConfig{
			"openai": {
				API:    "chat",
				APIKey: "sk-base",
				Extra:  &ProviderExtra{UserAgent: "base-client/1.0"},
			},
		},
	}
	override := Settings{
		Providers: map[string]*ProviderConfig{
			"openai": {
				API:   "responses",
				Extra: &ProviderExtra{UserAgent: "override-client/1.0"},
			},
		},
	}

	merged := mergeSettings(base, override)
	pc := merged.Providers["openai"]
	if pc.API != "responses" {
		t.Fatalf("API = %q, want responses", pc.API)
	}
	if pc.APIKey != "sk-base" {
		t.Fatalf("APIKey = %q, want inherited key", pc.APIKey)
	}
	if got := pc.Extra.UserAgent; got != "override-client/1.0" {
		t.Fatalf("Extra.UserAgent = %q, want override-client/1.0", got)
	}
}

func TestConnection(t *testing.T) {
	headers := map[string]string{"Anthropic-Beta": "explicit"}
	pc := ProviderConfig{
		API:    "responses",
		APIKey: "key",
		Extra: &ProviderExtra{
			UserAgent:     "codebot-test/1.0",
			Headers:       headers,
			AnthropicBeta: "alias",
		},
	}
	conn := pc.connection()
	if conn.API != "responses" || conn.APIKey != "key" || conn.UserAgent != "codebot-test/1.0" {
		t.Fatalf("connection = %+v", conn)
	}
	// The explicit header wins over the alias.
	if len(conn.Headers) != 1 || conn.Headers["Anthropic-Beta"] != "explicit" {
		t.Fatalf("headers = %v", conn.Headers)
	}

	pc.Extra = &ProviderExtra{AnthropicBeta: "alias", Headers: headers}
	delete(headers, "Anthropic-Beta")
	if conn := pc.connection(); conn.Headers["anthropic-beta"] != "alias" || len(headers) != 0 {
		t.Fatalf("headers = %v; settings headers = %v", conn.Headers, headers)
	}
}

func TestConnectionBedrock(t *testing.T) {
	pc := ProviderConfig{Extra: &ProviderExtra{Region: "eu-west-1", AccessKeyID: "AKID", SecretAccessKey: "secret"}}
	if !pc.HasCredentials() {
		t.Fatal("AWS keys should count as credentials")
	}
	conn := pc.connection()
	creds, err := conn.Credentials.Credentials(context.Background())
	if err != nil || conn.Region != "eu-west-1" || creds.AccessKeyID != "AKID" || creds.SecretAccessKey != "secret" {
		t.Fatalf("connection = %+v, credentials = %+v, %v", conn, creds, err)
	}
	if (ProviderConfig{}).HasCredentials() {
		t.Fatal("empty provider has no credentials")
	}
}

func TestValidateResolved(t *testing.T) {
	if err := validateResolved(Settings{}.resolve()); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	for name, change := range map[string]func(*Resolved){
		"provider api":                 func(r *Resolved) { r.Providers["openai"] = ProviderConfig{API: "legacy"} },
		"api on a non-OpenAI provider": func(r *Resolved) { r.Providers["anthropic"] = ProviderConfig{API: "responses"} },
		"negative compact window":      func(r *Resolved) { r.CompactWindow = -1 },
		"compact ratio of 1":           func(r *Resolved) { r.CompactRatio = 1 },
		"negative compact ratio":       func(r *Resolved) { r.CompactRatio = -0.5 },
		"search provider":              func(r *Resolved) { r.SearchProvider = "bing" },
	} {
		r := Settings{}.resolve()
		change(&r)
		if validateResolved(r) == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// Every field of Settings is in one class of what a project may set, so a
// new one cannot slip in unclassified: see ForProject.
func TestForProjectClassifiesEveryField(t *testing.T) {
	class := map[string]string{
		"provider": "safe", "model": "safe", "reasoning_effort": "safe", "max_turns": "safe",
		"compact_window": "safe", "compact_ratio": "safe", "snapshot": "safe",
		"hooks": "grant", "mcp_servers": "grant", "plugins": "grant", "permissions": "grant",
		"providers": "never", "search_provider": "never", "search_api_key": "never", "telemetry": "never", "marketplaces": "never",
	}
	typ := reflect.TypeFor[Settings]()
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if class[name] == "" {
			t.Errorf("settings field %q has no class", name)
		}
	}

	// A project setting every field.
	str, n, ratio, yes := "x", 1, 0.5, true
	s := Settings{
		Provider: &str, Model: &str, ReasoningEffort: &str, MaxTurns: &n,
		CompactWindow: &n, CompactRatio: &ratio, Snapshot: &yes,
		Hooks:          HooksConfig{"PreToolUse": {{Type: "command", Command: "x"}}},
		MCPServers:     map[string]MCPServer{"db": {Command: "x"}},
		Plugins:        []string{"github.com/acme/tools"},
		Permissions:    &PermissionsConfig{Allow: []string{"Bash(x)"}, Deny: []string{"Bash(y)"}, ReadRoots: []string{"/r"}, WriteRoots: []string{"/w"}},
		Providers:      map[string]*ProviderConfig{"p": {BaseURL: "https://evil"}},
		SearchProvider: &str, SearchAPIKey: &str, Telemetry: &TelemetryConfig{Enabled: true},
		Marketplaces: []string{"github.com/acme/plugins"},
	}
	for _, trusted := range []bool{false, true} {
		kept, refused := ForProject(s, trusted)
		if !slices.Equal(refused, []string{"marketplaces", "providers", "search_api_key", "search_provider", "telemetry"}) {
			t.Errorf("trusted=%v: refused %q", trusted, refused)
		}
		if kept.Providers != nil || kept.SearchProvider != nil || kept.SearchAPIKey != nil || kept.Telemetry != nil || kept.Marketplaces != nil {
			t.Errorf("trusted=%v: kept a field a project may not set", trusted)
		}
		if kept.Model == nil || kept.Snapshot == nil || kept.Permissions == nil || !slices.Equal(kept.Permissions.Deny, []string{"Bash(y)"}) {
			t.Errorf("trusted=%v: dropped a safe field: %+v", trusted, kept)
		}
		granted := kept.Hooks != nil && kept.MCPServers != nil && kept.Plugins != nil && kept.Permissions.Allow != nil && kept.Permissions.ReadRoots != nil && kept.Permissions.WriteRoots != nil
		if granted != trusted {
			t.Errorf("trusted=%v: grants kept = %v", trusted, granted)
		}
	}
}

// Merging leaves the layers as they were: they are merged again on reload.
func TestMergeLeavesItsInputs(t *testing.T) {
	base := Settings{
		Providers:   map[string]*ProviderConfig{"p": {APIKey: "k"}},
		Hooks:       HooksConfig{"Stop": {{Command: "a"}}},
		Permissions: &PermissionsConfig{Allow: make([]string, 1, 4)},
	}
	override := Settings{
		Providers:   map[string]*ProviderConfig{"p": {BaseURL: "u"}},
		Hooks:       HooksConfig{"Stop": {{Command: "b"}}},
		Permissions: &PermissionsConfig{Allow: []string{"x"}},
	}
	mergeSettings(base, override)
	if base.Providers["p"].BaseURL != "" || len(base.Hooks["Stop"]) != 1 || len(base.Permissions.Allow) != 1 {
		t.Errorf("merging changed the base: %+v %+v %+v", base.Providers["p"], base.Hooks, base.Permissions)
	}
}

func TestProjectRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ProjectRoot(sub); got != root {
		t.Errorf("root of %s = %s, want %s", sub, got, root)
	}
	alone := t.TempDir()
	if got := ProjectRoot(alone); got != alone {
		t.Errorf("root outside a repository = %s, want %s", got, alone)
	}
	t.Setenv("HOME", alone)
	if got := ProjectRoot(alone); got != "" {
		t.Errorf("the home directory is the project %s", got)
	}
}

// A project's roots are relative to its root, wherever in it codebot runs;
// the user's, to where it runs.
func TestProjectRootsAreTheProjects(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "sub")
	l := Layers{
		Root:    root,
		User:    Settings{Permissions: &PermissionsConfig{WriteRoots: []string{"."}}},
		Project: Settings{Permissions: &PermissionsConfig{WriteRoots: []string{"../shared"}}},
	}
	r, err := l.Resolve(cwd, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{cwd, filepath.Join(filepath.Dir(root), "shared")}
	if !slices.Equal(r.Permissions.WriteRoots, want) {
		t.Errorf("write roots %q, want %q", r.Permissions.WriteRoots, want)
	}
}
