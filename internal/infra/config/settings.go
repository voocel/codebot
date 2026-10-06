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

// ConfigDir is the project-level config directory name.
const ConfigDir = ".codebot"

// ProviderConfig holds credentials and model configuration for a single provider.
type ProviderConfig struct {
	Type       string         `json:"type,omitempty"` // protocol type: a LiteLLM provider, "gateway" for a model gateway; required only when the provider name is not a known litellm provider
	API        string         `json:"api,omitempty"`  // OpenAI protocol endpoint: chat (default) or responses
	APIKey     string         `json:"api_key,omitempty"`
	BaseURL    string         `json:"base_url,omitempty"`
	Models     []string       `json:"models,omitempty"`      // available model list for this provider
	SmallModel string         `json:"small_model,omitempty"` // lightweight model for sub-agents
	Extra      *ProviderExtra `json:"extra,omitempty"`
}

// ProviderExtra holds the provider's connection settings beyond the API key
// and base URL. They configure the HTTP client, never the request body.
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

// HasCredentials reports whether the provider has an API key, or AWS keys
// for bedrock.
func (pc ProviderConfig) HasCredentials() bool {
	return pc.APIKey != "" || pc.Extra != nil && pc.Extra.AccessKeyID != ""
}

// connection returns the settings for reaching the provider.
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

// hasHeader reports whether headers sets name, compared case-insensitively.
func hasHeader(headers map[string]string, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

// ModelSpec resolves how to build model from the provider configured under
// name; a name missing from providers must be a built-in provider type.
func ModelSpec(providers map[string]ProviderConfig, name, model string) (provider.ModelSpec, error) {
	typ, err := resolveConfiguredProviderType(providers, name)
	if err != nil {
		return provider.ModelSpec{}, err
	}
	return provider.ModelSpec{Provider: name, Type: typ, Model: model, Conn: providers[name].connection()}, nil
}

// TelemetryConfig configures OpenTelemetry trace export to an OTLP backend
// (e.g. Langfuse). Telemetry stays off unless Enabled is true.
type TelemetryConfig struct {
	Enabled   bool   `json:"enabled,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`   // OTLP/HTTP endpoint URL, e.g. https://cloud.langfuse.com/api/public/otel
	PublicKey string `json:"public_key,omitempty"` // basic-auth username
	SecretKey string `json:"secret_key,omitempty"` // basic-auth password
}

// providerType resolves the protocol type for this provider.
// The protocol type maps to a name registered in litellm's provider registry.
func (pc ProviderConfig) providerType(name string) (string, error) {
	return resolveProviderType(name, pc.Type)
}

// resolveProviderType resolves a provider's protocol type. When explicitType
// is set it wins (and must be registered); otherwise the provider name itself
// must be a registered litellm provider.
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

// resolveConfiguredProviderType resolves the protocol type for a configured provider.
func resolveConfiguredProviderType(providers map[string]ProviderConfig, name string) (string, error) {
	if pc, ok := providers[name]; ok {
		return pc.providerType(name)
	}
	return resolveProviderType(name, "")
}

// HookEntry describes a single hook.
// Supported types: "command" (shell), "prompt" (LLM evaluation), "http" (POST).
type HookEntry struct {
	Type           string            `json:"type"`                      // "command", "prompt", or "http"
	Command        string            `json:"command,omitempty"`         // type=command: sh command
	CommandWindows string            `json:"command_windows,omitempty"` // type=command: PowerShell command run on Windows instead
	Prompt         string            `json:"prompt,omitempty"`          // type=prompt: LLM prompt ($ARGUMENTS = payload)
	URL            string            `json:"url,omitempty"`             // type=http: POST endpoint
	Headers        map[string]string `json:"headers,omitempty"`         // type=http: request headers
	Matcher        string            `json:"matcher,omitempty"`         // tool name filter: exact (case-insensitive) or /regex/
	If             string            `json:"if,omitempty"`              // tool arguments JSON filter: /regex/, or the exact JSON
	Blocking       *bool             `json:"blocking,omitempty"`        // can block execution
	Timeout        *int              `json:"timeout,omitempty"`         // seconds (default 60)
	// Env is set by codebot alone, for a command hook's process: a plugin's
	// hooks get PLUGIN_ROOT and PLUGIN_DATA.
	Env map[string]string `json:"-"`
}

// HooksConfig maps event names to their hook entries.
type HooksConfig map[string][]HookEntry

// MCPServer describes a single MCP server connection.
//
// Stdio example (default):
//
//	{"command": "npx", "args": ["-y", "@upstash/context7-mcp"], "env": {"KEY": "${VAR}"}}
//
// HTTP example:
//
//	{"type": "http", "url": "https://mcp.example.com/mcp", "headers": {"Authorization": "Bearer ${TOKEN}"}}
type MCPServer struct {
	Type    string            `json:"type,omitempty"` // "stdio" (default) or "http"
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"` // a stdio server's working directory
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Check checks that s is one kind of server: a stdio one runs a command and
// calls no URL; an http one calls a URL and runs nothing.
func (s MCPServer) Check() error {
	switch s.Type {
	case "", "stdio":
		if s.Command == "" {
			return errors.New(`a stdio server needs a command; a remote one, "type": "http"`)
		}
		if s.URL != "" || s.Headers != nil {
			return errors.New("a stdio server takes no url or headers")
		}
	case "http":
		if s.URL == "" {
			return errors.New("an http server needs a url")
		}
		if s.Command != "" || s.Args != nil || s.Env != nil || s.Cwd != "" {
			return errors.New("an http server takes no command, args, env or cwd")
		}
	default:
		return fmt.Errorf("unknown type %q", s.Type)
	}
	return nil
}

// Settings holds application-level configuration.
// Fields use pointer types so unset fields fall back to defaults.
type Settings struct {
	Provider        *string                    `json:"provider,omitempty"`         // provider name (matches key in providers map)
	Model           *string                    `json:"model,omitempty"`            // model name sent to API as-is
	ReasoningEffort *string                    `json:"reasoning_effort,omitempty"` // "" = provider default; off | low | medium | high | xhigh | max
	Providers       map[string]*ProviderConfig `json:"providers,omitempty"`

	MaxTurns *int `json:"max_turns,omitempty"`

	// CompactWindow caps the effective context window used for compaction.
	// Effective = min(model's detected window, CompactWindow). 0 = disabled.
	CompactWindow *int `json:"compact_window,omitempty"`
	// CompactRatio triggers compaction when usage >= effective * ratio.
	// Range (0, 1). Unset leaves room for the model's reply instead.
	CompactRatio *float64 `json:"compact_ratio,omitempty"`

	SearchProvider *string `json:"search_provider,omitempty"`
	SearchAPIKey   *string `json:"search_api_key,omitempty"`

	Hooks HooksConfig `json:"hooks,omitempty"` // lifecycle hooks

	// MCPServers are the MCP servers to connect, by name; a project entry
	// replaces the global one of the same name.
	MCPServers map[string]MCPServer `json:"mcp_servers,omitempty"`

	// Plugins are the plugins to load, each a git repository
	// ("host/owner/repo", or an https or ssh URL, "#ref" pinning a tag,
	// branch or commit) or a local directory, relative to the directory of
	// the settings file declaring it.
	Plugins []string `json:"plugins,omitempty"`

	Permissions *PermissionsConfig `json:"permissions,omitempty"`

	Telemetry *TelemetryConfig `json:"telemetry,omitempty"` // OpenTelemetry trace export

	// Snapshot toggles workspace file checkpoints backing /undo. Unset means on;
	// set false to disable (e.g. on a large repo where per-turn scans lag).
	Snapshot *bool `json:"snapshot,omitempty"`
}

// PermissionsConfig holds user-defined permission rules.
type PermissionsConfig struct {
	Allow      []string `json:"allow,omitempty"`
	Deny       []string `json:"deny,omitempty"`
	ReadRoots  []string `json:"read_roots,omitempty"`
	WriteRoots []string `json:"write_roots,omitempty"`
}

// Resolved holds settings resolved to concrete values (no pointers).
type Resolved struct {
	Provider  string                    // active provider name
	Model     string                    // model name sent to API as-is
	Providers map[string]ProviderConfig // per-provider credentials

	CompactWindow   int     // user-configured cap on effective window; 0 = disabled
	CompactRatio    float64 // usage ratio that triggers compaction; 0 = unset
	ReasoningEffort string
	MaxTurns        int
	SearchProvider  string
	SearchAPIKey    string

	Permissions PermissionsConfig // user-defined permission rules

	Telemetry TelemetryConfig // OTLP trace export config

	Snapshot bool // workspace checkpoints for /undo; defaults on
}

// FormatModelID combines provider and model into "provider/model".
// If model already contains "/", it is returned as-is.
func FormatModelID(provider, model string) string {
	if provider == "" || strings.Contains(model, "/") {
		return model
	}
	return provider + "/" + model
}

// resolve converts Settings to Resolved using defaults for unset fields.
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

// validateResolved rejects unsupported values after global/project settings
// have been merged and defaults applied.
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

// ProjectSettingsPath returns <root>/.codebot/settings.json.
func ProjectSettingsPath(root string) string {
	return filepath.Join(root, ConfigDir, "settings.json")
}

// UserSettingsPath returns ~/.codebot/settings.json.
func UserSettingsPath() string {
	return filepath.Join(UserConfigDir(), "settings.json")
}

// ProjectRoot returns the root of the project cwd is in: the top of the git
// repository holding it, else cwd itself. The home directory is no project:
// its .codebot is the user's, so there ProjectRoot returns "".
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

// SessionsDir returns ~/.codebot/projects/<projectID>/.
// Sessions are stored globally but scoped by project.
func SessionsDir(cwd string) string {
	return filepath.Join(UserConfigDir(), "projects", projectID(cwd))
}

// SnapshotDir returns ~/.codebot/snapshot/<projectID> — the shadow git
// repository backing /undo file checkpoints for this project.
func SnapshotDir(cwd string) string {
	return filepath.Join(UserConfigDir(), "snapshot", projectID(cwd))
}

// UndoStatePath returns the per-session sidecar that persists /undo's snapshot
// stack across restarts: ~/.codebot/projects/<projectID>/<sessionID>/undo-stack.json.
// It sits under the per-session dir alongside bg/ and tool-outputs/.
func UndoStatePath(cwd, sessionID string) string {
	return filepath.Join(SessionsDir(cwd), sessionID, "undo-stack.json")
}

// ApprovalsPath returns ~/.codebot/approvals/<projectID>.json.
func ApprovalsPath(cwd string) string {
	return filepath.Join(UserConfigDir(), "approvals", projectID(cwd)+".json")
}

// AuditLogPath returns ~/.codebot/audit.log.
func AuditLogPath() string {
	return filepath.Join(UserConfigDir(), "audit.log")
}

var nonAlphaNum = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// projectID returns a stable, human-readable directory name for a project path.
// Format: non-alphanumeric characters replaced with "-" (e.g. /Users/me/proj → -Users-me-proj).
func projectID(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	return nonAlphaNum.ReplaceAllString(abs, "-")
}

// UserConfigDir returns ~/.codebot/.
func UserConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ConfigDir)
}

// Layers are the settings as their files hold them: the user's, in
// ~/.codebot/settings.json, and the project's, in .codebot/settings.json at
// the project's root. A project is shared and may come from anyone, so it
// sets only some of the fields, some only once trusted; see ForProject.
type Layers struct {
	Root    string // the project's root, "" for none; see ProjectRoot
	User    Settings
	Project Settings
}

// Load reads the user's and the project's settings. It fails when a
// settings file exists and cannot be parsed.
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

// inProject reads the files of the project at root, which may not lead
// outside it: its settings are its own, never a file of the user's.
func inProject(root string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) { return regular.ReadFileIn(root, path) }
}

// Resolve combines the user's settings and the project's over them: those
// a project sets as it likes, and of its grants, granted, those the user
// agreed to (see extension.Load). It applies the defaults and validates the
// result. The extensions, hooks and MCP servers among them, are left to the
// extension package. Model is deliberately never defaulted — hardcoded
// model names go stale; boot validates it is set.
func (l Layers) Resolve(cwd string, granted Settings) (Resolved, error) {
	project, _, _ := ForProject(l.Project)
	var perms PermissionsConfig
	if p := project.Permissions; p != nil {
		perms.Deny = p.Deny
	}
	if g := granted.Permissions; g != nil {
		// The project's roots are its own: relative to its root, wherever
		// in it codebot runs.
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

// ForProject sorts a project's settings s by what a project may do with
// them. It sets as it likes those that only shape how codebot works, open,
// deny rules among them. What lets code run or calls through unasked — its
// hooks, MCP servers, plugins, allow rules and roots — are grants, each of
// which takes effect once the user agrees to it. Where calls go and whose
// credentials they carry are the user's alone: the providers, the search
// provider and its key, and telemetry are refused, named and dropped.
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

// mergeSettings merges override over base: of the fields Resolved holds,
// those set in override take precedence, permission rules and roots add up.
// The others, the extensions, keep base's. Neither changes.
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
			// Field-level merge: override only non-zero fields.
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

// EditUserSettings applies edit to the user's settings, creating the file
// if need be.
func EditUserSettings(edit func(*Settings)) error {
	return editSettings(UserSettingsPath(), regular.ReadFile, edit)
}

// EditProjectSettings applies edit to the settings of the project at root,
// creating the file if need be. A file, or its directory, leading outside
// the project is not the project's to edit.
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

// editSettings applies edit to the settings file at path, read with read.
// A symlink stays one: the file it leads to is written.
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

// PatchUserSettings applies the non-nil fields of patch to the user's
// settings. What the user picks as codebot runs is theirs, never the
// project's, which is shared.
func PatchUserSettings(patch Settings) error {
	return EditUserSettings(func(s *Settings) { *s = mergeSettings(*s, patch) })
}

// LockFile takes the lock codebot's processes share to edit the file at
// path, so that none of them loses another's edit. The locks are kept in
// the user's config directory, never in a project.
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

// WriteFileAtomic writes data to path whole or not at all: a reader never sees
// it half written.
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

// loadFile reads the settings file at path with read; none is no settings.
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

// normalizePermissionRoots makes the roots absolute, the workspace when none
// are set; whatever is writable is readable.
func normalizePermissionRoots(cwd string, perms PermissionsConfig) PermissionsConfig {
	perms.WriteRoots = normalizeRoots(cwd, perms.WriteRoots)
	perms.ReadRoots = normalizeRoots(cwd, append(normalizeRoots(cwd, perms.ReadRoots), perms.WriteRoots...))
	return perms
}

// normalizeRoots makes roots absolute against cwd, without duplicates; no
// roots means cwd.
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

// absRoots returns roots absolute, those relative taken from base.
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
