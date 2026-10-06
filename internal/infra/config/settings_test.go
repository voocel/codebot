package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

// Every field of Settings is in one class of what a project may do with
// it, so a new one cannot slip in unclassified: see ForProject.
func TestForProjectClassifiesEveryField(t *testing.T) {
	class := map[string]string{
		"provider": "open", "model": "open", "reasoning_effort": "open", "max_turns": "open",
		"compact_window": "open", "compact_ratio": "open", "snapshot": "open",
		"hooks": "grant", "mcp_servers": "grant", "plugins": "grant", "permissions": "grant",
		"providers": "refused", "search_provider": "refused", "search_api_key": "refused", "telemetry": "refused",
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
	}
	open, grants, refused := ForProject(s)
	if !slices.Equal(refused, []string{"providers", "search_api_key", "search_provider", "telemetry"}) {
		t.Errorf("refused %q", refused)
	}
	if open.Providers != nil || open.SearchProvider != nil || open.SearchAPIKey != nil || open.Telemetry != nil ||
		grants.Providers != nil || grants.SearchProvider != nil || grants.SearchAPIKey != nil || grants.Telemetry != nil {
		t.Error("kept a field a project may not set")
	}
	if open.Model == nil || open.Snapshot == nil || open.Permissions == nil || !slices.Equal(open.Permissions.Deny, []string{"Bash(y)"}) {
		t.Errorf("dropped an open field: %+v", open)
	}
	if open.Hooks != nil || open.MCPServers != nil || open.Plugins != nil || open.Permissions.Allow != nil || open.Permissions.ReadRoots != nil || open.Permissions.WriteRoots != nil {
		t.Errorf("a grant is open: %+v %+v", open, open.Permissions)
	}
	if grants.Hooks == nil || grants.MCPServers == nil || grants.Plugins == nil || grants.Permissions.Allow == nil || grants.Permissions.ReadRoots == nil || grants.Permissions.WriteRoots == nil || grants.Permissions.Deny != nil {
		t.Errorf("grants %+v %+v", grants, grants.Permissions)
	}

	// Resolving takes the grants given alone.
	effort := "high"
	s.ReasoningEffort = &effort
	r, err := Layers{Root: t.TempDir(), Project: s}.Resolve(t.TempDir(), Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Permissions.Allow) > 0 || !slices.Equal(r.Permissions.Deny, []string{"Bash(y)"}) || r.Providers["p"].BaseURL != "" || r.Telemetry.Enabled {
		t.Errorf("resolved %+v", r)
	}
}

// Merging leaves the layers as they were: they are merged again on reload.
func TestMergeLeavesItsInputs(t *testing.T) {
	base := Settings{
		Providers:   map[string]*ProviderConfig{"p": {APIKey: "k"}},
		Permissions: &PermissionsConfig{Allow: make([]string, 1, 4)},
	}
	override := Settings{
		Providers:   map[string]*ProviderConfig{"p": {BaseURL: "u"}},
		Permissions: &PermissionsConfig{Allow: []string{"x"}},
	}
	mergeSettings(base, override)
	if base.Providers["p"].BaseURL != "" || len(base.Permissions.Allow) != 1 {
		t.Errorf("merging changed the base: %+v %+v", base.Providers["p"], base.Permissions)
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
		Root: root,
		User: Settings{Permissions: &PermissionsConfig{WriteRoots: []string{"."}}},
	}
	r, err := l.Resolve(cwd, Settings{Permissions: &PermissionsConfig{WriteRoots: []string{"../shared"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{cwd, filepath.Join(filepath.Dir(root), "shared")}
	if !slices.Equal(r.Permissions.WriteRoots, want) {
		t.Errorf("write roots %q, want %q", r.Permissions.WriteRoots, want)
	}
}

// A project's settings are its own: a file leading outside it is refused,
// and editing one writes where it leads.
func TestProjectSettingsStayInTheProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ConfigDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserSettingsPath(), []byte(`{"providers":{"p":{"api_key":"sk-secret"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ConfigDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(UserSettingsPath(), ProjectSettingsPath(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Error("loaded project settings leading to the user's")
	}
	if err := EditProjectSettings(root, func(s *Settings) { s.Plugins = []string{"./kit"} }); err == nil {
		t.Error("edited project settings leading to the user's")
	}
	if data, _ := os.ReadFile(UserSettingsPath()); strings.Contains(string(data), "kit") {
		t.Errorf("the user's settings changed: %s", data)
	}

	// The user's settings may be a link, to their dotfiles say, which stays.
	dotfiles := filepath.Join(home, "dotfiles.json")
	if err := os.Rename(UserSettingsPath(), dotfiles); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfiles, UserSettingsPath()); err != nil {
		t.Fatal(err)
	}
	if err := EditUserSettings(func(s *Settings) { s.Plugins = []string{"~/kit"} }); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(UserSettingsPath()); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v", err)
	}
	if data, _ := os.ReadFile(dotfiles); !strings.Contains(string(data), "~/kit") || !strings.Contains(string(data), "sk-secret") {
		t.Errorf("dotfiles %s", data)
	}
}
