package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/voocel/codebot/internal/infra/provider"
	"github.com/voocel/codebot/internal/lib/filelock"
	"github.com/voocel/codebot/internal/lib/regular"
	llmprovider "github.com/voocel/litellm/provider"
	"github.com/voocel/litellm/provider/bedrock"
)

const ConfigDir = ".codebot"

type ProviderConfig struct {
	Type       string         `json:"type,omitempty"` // a litellm provider or "gateway"; required when the name is not a known provider
	API        string         `json:"api,omitempty"`  // OpenAI protocol endpoint: chat (default) or responses
	APIKey     string         `json:"api_key,omitempty"`
	BaseURL    string         `json:"base_url,omitempty"`
	Models     []string       `json:"models,omitempty"`
	SmallModel string         `json:"small_model,omitempty"` // lightweight model for sub-agents
	Extra      *ProviderExtra `json:"extra,omitempty"`
}

// ProviderExtra configures the HTTP client, never the request body.
type ProviderExtra struct {
	UserAgent string            `json:"user_agent,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	// AnthropicBeta sets the anthropic-beta header unless Headers sets it.
	AnthropicBeta string `json:"anthropic_beta,omitempty"`
	// Region and the AWS keys authenticate bedrock, which takes no API key.
	Region          string `json:"region,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`
}

func (pc ProviderConfig) connection() llmprovider.Config {
	conn := llmprovider.Config{APIKey: pc.APIKey, BaseURL: pc.BaseURL, API: pc.API}
	x := pc.Extra
	if x == nil {
		return conn
	}
	conn.UserAgent = x.UserAgent
	conn.Headers = maps.Clone(x.Headers)
	if x.AnthropicBeta != "" && !hasHeader(conn.Headers, "anthropic-beta") {
		if conn.Headers == nil {
			conn.Headers = make(map[string]string, 1)
		}
		conn.Headers["anthropic-beta"] = x.AnthropicBeta
	}
	conn.Region = x.Region
	if x.AccessKeyID != "" {
		conn.Credentials = bedrock.StaticCredentials(x.AccessKeyID, x.SecretAccessKey, x.SessionToken)
	}
	return conn
}

func hasHeader(headers map[string]string, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

// ModelSpec accepts a name missing from providers only if it is a built-in
// provider type.
func ModelSpec(providers map[string]ProviderConfig, name, model string) (provider.ModelSpec, error) {
	typ, err := resolveConfiguredProviderType(providers, name)
	if err != nil {
		return provider.ModelSpec{}, err
	}
	return provider.ModelSpec{Provider: name, Type: typ, Model: model, Conn: providers[name].connection()}, nil
}

type TelemetryConfig struct {
	Enabled   bool   `json:"enabled,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`   // OTLP/HTTP endpoint URL
	PublicKey string `json:"public_key,omitempty"` // basic-auth username
	SecretKey string `json:"secret_key,omitempty"` // basic-auth password
}

func (pc ProviderConfig) providerType(name string) (string, error) {
	return resolveProviderType(name, pc.Type)
}

func resolveProviderType(name, explicitType string) (string, error) {
	provType := strings.ToLower(strings.TrimSpace(explicitType))
	if provType != "" {
		if provider.IsSupportedType(provType) {
			return provType, nil
		}
		return "", fmt.Errorf("configuration error: providers.%s.type=%q is unsupported", name, explicitType)
	}
	lowered := strings.ToLower(strings.TrimSpace(name))
	if provider.IsSupportedType(lowered) {
		return lowered, nil
	}
	return "", fmt.Errorf("configuration error: providers.%s.type is required for custom providers", name)
}

func resolveConfiguredProviderType(providers map[string]ProviderConfig, name string) (string, error) {
	if pc, ok := providers[name]; ok {
		return pc.providerType(name)
	}
	return resolveProviderType(name, "")
}

type HookEntry struct {
	Type           string            `json:"type"`                      // "command", "prompt", or "http"
	Command        string            `json:"command,omitempty"`         // type=command: sh command
	CommandWindows string            `json:"command_windows,omitempty"` // type=command: PowerShell command run on Windows instead
	Prompt         string            `json:"prompt,omitempty"`          // type=prompt: LLM prompt ($ARGUMENTS = payload)
	URL            string            `json:"url,omitempty"`             // type=http: POST endpoint
	Headers        map[string]string `json:"headers,omitempty"`         // type=http
	Matcher        string            `json:"matcher,omitempty"`         // tool name filter: exact (case-insensitive) or /regex/
	If             string            `json:"if,omitempty"`              // tool arguments JSON filter: /regex/, or the exact JSON
	Blocking       *bool             `json:"blocking,omitempty"`        // can block execution
	Timeout        *int              `json:"timeout,omitempty"`         // seconds (default 60)
	// Env is set by codebot, never read from settings: plugin hooks get
	// PLUGIN_ROOT and PLUGIN_DATA.
	Env map[string]string `json:"-"`
}

// HooksConfig is keyed by event name.
type HooksConfig map[string][]HookEntry

type MCPServer struct {
	Type    string            `json:"type,omitempty"` // "stdio" (default) or "http"
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	OAuth   *MCPOAuth         `json:"oauth,omitempty"`
}

// MCPOAuth is a client registered beforehand with the authorization server
// of an http server, for one that takes no client metadata document.
type MCPOAuth struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

func (s MCPServer) Check() error {
	switch s.Type {
	case "", "stdio":
		if s.Command == "" {
			return errors.New(`a stdio server needs a command; a remote one, "type": "http"`)
		}
		if s.URL != "" || s.Headers != nil || s.OAuth != nil {
			return errors.New("a stdio server takes no url, headers or oauth")
		}
	case "http":
		if s.URL == "" {
			return errors.New("an http server needs a url")
		}
		if s.Command != "" || s.Args != nil || s.Env != nil || s.Cwd != "" {
			return errors.New("an http server takes no command, args, env or cwd")
		}
		if s.OAuth != nil && s.OAuth.ClientID == "" {
			return errors.New("oauth needs a client_id")
		}
		if s.OAuth != nil && hasHeader(s.Headers, "Authorization") {
			return errors.New("an http server takes oauth or an Authorization header, not both")
		}
	default:
		return fmt.Errorf("unknown type %q", s.Type)
	}
	return nil
}

// Settings uses pointers so that unset fields fall back to defaults.
type Settings struct {
	Provider        *string                    `json:"provider,omitempty"`         // a key of Providers
	Model           *string                    `json:"model,omitempty"`            // model name sent to API as-is
	ReasoningEffort *string                    `json:"reasoning_effort,omitempty"` // "" = provider default; off | low | medium | high | xhigh | max
	Providers       map[string]*ProviderConfig `json:"providers,omitempty"`

	MaxTurns *int `json:"max_turns,omitempty"`

	// CompactWindow caps the model's context window for compaction; 0 means
	// no cap.
	CompactWindow *int `json:"compact_window,omitempty"`
	// CompactRatio, in (0, 1), compacts when usage reaches window * ratio.
	// Unset reserves room for the model's reply instead.
	CompactRatio *float64 `json:"compact_ratio,omitempty"`

	SearchProvider *string `json:"search_provider,omitempty"`
	SearchAPIKey   *string `json:"search_api_key,omitempty"`

	Hooks HooksConfig `json:"hooks,omitempty"`

	// A project entry replaces the user's entry of the same name.
	MCPServers map[string]MCPServer `json:"mcp_servers,omitempty"`

	// Plugins are git repositories ("host/owner/repo" or an https or ssh URL,
	// with "#ref" pinning a tag, branch or commit) or local directories
	// relative to the settings file that declares them.
	Plugins []string `json:"plugins,omitempty"`

	Permissions *PermissionsConfig `json:"permissions,omitempty"`

	Telemetry *TelemetryConfig `json:"telemetry,omitempty"`

	// Snapshot enables the checkpoints behind /rewind; unset means on. Large
	// repos may turn it off because every turn scans the workspace.
	Snapshot *bool `json:"snapshot,omitempty"`
}

type PermissionsConfig struct {
	Allow      []string `json:"allow,omitempty"`
	Deny       []string `json:"deny,omitempty"`
	ReadRoots  []string `json:"read_roots,omitempty"`
	WriteRoots []string `json:"write_roots,omitempty"`
}

type Resolved struct {
	Provider  string
	Model     string
	Providers map[string]ProviderConfig

	CompactWindow   int     // 0 = no cap
	CompactRatio    float64 // 0 = unset
	ReasoningEffort string
	MaxTurns        int
	SearchProvider  string
	SearchAPIKey    string

	Permissions PermissionsConfig

	Telemetry TelemetryConfig

	Snapshot bool
}

func FormatModelID(provider, model string) string {
	if provider == "" || strings.Contains(model, "/") {
		return model
	}
	return provider + "/" + model
}

func (s Settings) resolve() Resolved {
	r := Resolved{
		Provider:       "openai",
		Providers:      make(map[string]ProviderConfig),
		MaxTurns:       200,
		SearchProvider: "tavily",
		Snapshot:       true,
	}
	if s.Provider != nil && *s.Provider != "" {
		r.Provider = *s.Provider
	}
	if s.Model != nil {
		r.Model = *s.Model
	}
	for k, v := range s.Providers {
		if v != nil {
			r.Providers[k] = *v
		}
	}
	if s.ReasoningEffort != nil {
		r.ReasoningEffort = *s.ReasoningEffort
	}
	if s.MaxTurns != nil {
		r.MaxTurns = *s.MaxTurns
	}
	if s.CompactWindow != nil {
		r.CompactWindow = *s.CompactWindow
	}
	if s.CompactRatio != nil {
		r.CompactRatio = *s.CompactRatio
	}
	if s.SearchProvider != nil && *s.SearchProvider != "" {
		r.SearchProvider = *s.SearchProvider
	}
	if s.SearchAPIKey != nil {
		r.SearchAPIKey = *s.SearchAPIKey
	}
	if s.Permissions != nil {
		r.Permissions = *s.Permissions
	}
	if s.Telemetry != nil {
		r.Telemetry = *s.Telemetry
	}
	if s.Snapshot != nil {
		r.Snapshot = *s.Snapshot
	}
	return r
}

func validateResolved(r Resolved) error {
	if !provider.ValidEffort(r.ReasoningEffort) {
		return fmt.Errorf("configuration error: reasoning_effort=%q is unsupported; use empty string, off, low, medium, high, xhigh, or max", r.ReasoningEffort)
	}
	if r.CompactWindow < 0 {
		return fmt.Errorf("configuration error: compact_window=%d is negative", r.CompactWindow)
	}
	if r.CompactRatio < 0 || r.CompactRatio >= 1 {
		return fmt.Errorf("configuration error: compact_ratio=%g is out of range; use a value between 0 and 1", r.CompactRatio)
	}
	switch r.SearchProvider {
	case "tavily", "jina":
	default:
		return fmt.Errorf("configuration error: search_provider=%q is unsupported; use tavily or jina", r.SearchProvider)
	}
	for name, pc := range r.Providers {
		if err := validateProviderAPI(name, pc); err != nil {
			return err
		}
	}
	return nil
}

func validateProviderAPI(name string, pc ProviderConfig) error {
	switch pc.API {
	case "", "chat", "responses":
	default:
		return fmt.Errorf("configuration error: providers.%s.api=%q is unsupported; use chat or responses", name, pc.API)
	}
	if pc.API == "" {
		return nil
	}
	provType, err := pc.providerType(name)
	if err != nil {
		return err
	}
	if provType != "openai" {
		return fmt.Errorf("configuration error: providers.%s.api is only supported for OpenAI protocol providers", name)
	}
	return nil
}

func ProjectSettingsPath(root string) string {
	return filepath.Join(root, ConfigDir, "settings.json")
}

func UserSettingsPath() string {
	return filepath.Join(UserConfigDir(), "settings.json")
}

// ProjectRoot returns the top of the git repository holding cwd, else cwd.
// It returns "" for the home directory, whose .codebot belongs to the user.
func ProjectRoot(cwd string) string {
	cwd = filepath.Clean(cwd)
	root := cwd
	for dir := cwd; ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			root = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if home, _ := os.UserHomeDir(); root == home {
		return ""
	}
	return root
}

func SessionsDir(cwd string) string {
	return filepath.Join(UserConfigDir(), "projects", projectID(cwd))
}

func SnapshotDir(cwd string) string {
	return filepath.Join(UserConfigDir(), "snapshot", projectID(cwd))
}

func ApprovalsPath(cwd string) string {
	return filepath.Join(UserConfigDir(), "approvals", projectID(cwd)+".json")
}

func AuditLogPath() string {
	return filepath.Join(UserConfigDir(), "audit.log")
}

var nonAlphaNum = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func projectID(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	return nonAlphaNum.ReplaceAllString(abs, "-")
}

func UserConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ConfigDir)
}

// Layers are the raw user and project settings. A project may come from
// anyone, so it can set only some fields, and some only once trusted; see
// ForProject.
type Layers struct {
	Root    string // "" when there is no project
	User    Settings
	Project Settings
}

func Load(cwd string) (Layers, error) {
	l := Layers{Root: ProjectRoot(cwd)}
	var err error
	if UserConfigDir() != "" {
		if l.User, err = loadFile(UserSettingsPath(), regular.ReadFile); err != nil {
			return Layers{}, err
		}
	}
	if l.Root != "" {
		if l.Project, err = loadFile(ProjectSettingsPath(l.Root), inProject(l.Root)); err != nil {
			return Layers{}, err
		}
	}
	return l, nil
}

// inProject refuses paths that resolve outside root, so a project cannot
// point its settings at one of the user's files.
func inProject(root string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) { return regular.ReadFileIn(root, path) }
}

// Resolve lays the project's settings over the user's. Of the project's
// grants, only granted (those the user agreed to, see extension.Load)
// apply. Hooks, MCP servers and plugins are left to the extension package.
// Model has no default because hardcoded model names go stale.
func (l Layers) Resolve(cwd string, granted Settings) (Resolved, error) {
	project, _, _ := ForProject(l.Project)
	var perms PermissionsConfig
	if p := project.Permissions; p != nil {
		perms.Deny = p.Deny
	}
	if g := granted.Permissions; g != nil {
		// Project roots are relative to the project root, not cwd.
		perms.Allow, perms.ReadRoots, perms.WriteRoots = g.Allow, absRoots(l.Root, g.ReadRoots), absRoots(l.Root, g.WriteRoots)
	}
	project.Permissions = &perms
	r := mergeSettings(l.User, project).resolve()
	if err := validateResolved(r); err != nil {
		return Resolved{}, err
	}
	if r.SearchAPIKey == "" {
		r.SearchAPIKey = os.Getenv(strings.ToUpper(r.SearchProvider) + "_API_KEY")
	}
	r.Permissions = normalizePermissionRoots(cwd, r.Permissions)
	return r, nil
}

// ForProject splits a project's settings by what a project may do:
//   - open fields only shape how codebot works (deny rules included) and
//     apply as they are;
//   - grants run code or let calls through without asking (hooks, MCP
//     servers, plugins, allow rules, roots) and apply only once the user
//     agrees;
//   - fields that decide where calls go and whose credentials they carry
//     (providers, search provider and key, telemetry) belong to the user
//     alone, so they are dropped and listed in refused.
func ForProject(s Settings) (open, grants Settings, refused []string) {
	for name, set := range map[string]bool{
		"providers":       s.Providers != nil,
		"search_provider": s.SearchProvider != nil,
		"search_api_key":  s.SearchAPIKey != nil,
		"telemetry":       s.Telemetry != nil,
	} {
		if set {
			refused = append(refused, name)
		}
	}
	slices.Sort(refused)
	open = s
	open.Providers, open.SearchProvider, open.SearchAPIKey, open.Telemetry = nil, nil, nil, nil
	open.Hooks, open.MCPServers, open.Plugins, open.Permissions = nil, nil, nil, nil
	grants = Settings{Hooks: s.Hooks, MCPServers: s.MCPServers, Plugins: s.Plugins}
	if p := s.Permissions; p != nil {
		open.Permissions = &PermissionsConfig{Deny: p.Deny}
		grants.Permissions = &PermissionsConfig{Allow: p.Allow, ReadRoots: p.ReadRoots, WriteRoots: p.WriteRoots}
	}
	return open, grants, refused
}

// mergeSettings lays override over base; permission rules and roots are
// concatenated. Hooks, MCP servers and plugins keep base's values. Neither
// argument is modified.
func mergeSettings(base, override Settings) Settings {
	if override.Provider != nil {
		base.Provider = override.Provider
	}
	if override.Model != nil {
		base.Model = override.Model
	}
	if len(override.Providers) > 0 {
		base.Providers = maps.Clone(base.Providers)
		if base.Providers == nil {
			base.Providers = make(map[string]*ProviderConfig)
		}
		for k, v := range override.Providers {
			if v == nil {
				continue
			}
			if base.Providers[k] == nil {
				base.Providers[k] = v
				continue
			}
			existing := new(*base.Providers[k])
			if v.Type != "" {
				existing.Type = v.Type
			}
			if v.API != "" {
				existing.API = v.API
			}
			if v.APIKey != "" {
				existing.APIKey = v.APIKey
			}
			if v.BaseURL != "" {
				existing.BaseURL = v.BaseURL
			}
			if len(v.Models) > 0 {
				existing.Models = v.Models
			}
			if v.SmallModel != "" {
				existing.SmallModel = v.SmallModel
			}
			if v.Extra != nil {
				existing.Extra = v.Extra
			}
			base.Providers[k] = existing
		}
	}
	if override.ReasoningEffort != nil {
		base.ReasoningEffort = override.ReasoningEffort
	}
	if override.MaxTurns != nil {
		base.MaxTurns = override.MaxTurns
	}
	if override.CompactWindow != nil {
		base.CompactWindow = override.CompactWindow
	}
	if override.CompactRatio != nil {
		base.CompactRatio = override.CompactRatio
	}
	if override.SearchProvider != nil {
		base.SearchProvider = override.SearchProvider
	}
	if override.SearchAPIKey != nil {
		base.SearchAPIKey = override.SearchAPIKey
	}
	if o := override.Permissions; o != nil {
		var p PermissionsConfig
		if base.Permissions != nil {
			p = *base.Permissions
		}
		base.Permissions = &PermissionsConfig{
			Allow:      slices.Concat(p.Allow, o.Allow),
			Deny:       slices.Concat(p.Deny, o.Deny),
			ReadRoots:  slices.Concat(p.ReadRoots, o.ReadRoots),
			WriteRoots: slices.Concat(p.WriteRoots, o.WriteRoots),
		}
	}
	if override.Telemetry != nil {
		base.Telemetry = override.Telemetry
	}
	if override.Snapshot != nil {
		base.Snapshot = override.Snapshot
	}
	return base
}

// UserSettings reads the user's settings file alone; a missing one is empty.
func UserSettings() (Settings, error) {
	return loadFile(UserSettingsPath(), regular.ReadFile)
}

func EditUserSettings(edit func(*Settings)) error {
	return editSettings(UserSettingsPath(), regular.ReadFile, edit)
}

// EditProjectSettings refuses a settings file or directory that resolves
// outside the project.
func EditProjectSettings(root string, edit func(*Settings)) error {
	path := ProjectSettingsPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := regular.Within(root, filepath.Dir(path)); err != nil {
		return err
	}
	return editSettings(path, inProject(root), edit)
}

// editSettings writes through a symlink instead of replacing it.
func editSettings(path string, read func(string) ([]byte, error), edit func(*Settings)) error {
	unlock, err := LockFile(path)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := loadFile(path, read)
	if err != nil {
		return err
	}
	edit(&s)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	perm := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	return WriteFileAtomic(path, data, perm)
}

// PatchUserSettings writes the user's settings, never the project's: choices
// made while codebot runs must not land in a shared repository.
func PatchUserSettings(patch Settings) error {
	return EditUserSettings(func(s *Settings) { *s = mergeSettings(*s, patch) })
}

// LockFile serializes edits to path across codebot processes. Lock files
// live in the user's config directory, never in a project.
func LockFile(path string) (unlock func(), err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(UserConfigDir(), "locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(abs))
	return filelock.Lock(filepath.Join(dir, filepath.Base(abs)+"-"+hex.EncodeToString(sum[:6])+".lock"))
}

func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func loadFile(path string, read func(string) ([]byte, error)) (Settings, error) {
	var s Settings
	data, err := read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("configuration error: malformed settings.json (%s): %w", path, err)
	}
	return s, nil
}

func normalizePermissionRoots(cwd string, perms PermissionsConfig) PermissionsConfig {
	perms.WriteRoots = normalizeRoots(cwd, perms.WriteRoots)
	perms.ReadRoots = normalizeRoots(cwd, append(normalizeRoots(cwd, perms.ReadRoots), perms.WriteRoots...))
	return perms
}

func normalizeRoots(cwd string, roots []string) []string {
	var out []string
	for _, root := range absRoots(cwd, roots) {
		if !slices.Contains(out, root) {
			out = append(out, root)
		}
	}
	if len(out) == 0 {
		return []string{filepath.Clean(cwd)}
	}
	return out
}

func absRoots(base string, roots []string) []string {
	var out []string
	for _, root := range roots {
		if root = strings.TrimSpace(root); root == "" {
			continue
		}
		if root = expandHome(root); !filepath.IsAbs(root) {
			root = filepath.Join(base, root)
		}
		out = append(out, filepath.Clean(root))
	}
	return out
}

func expandHome(path string) string {
	if path == "" {
		return ""
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
