package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/voocel/codebot/internal/infra/provider"
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
	Type     string            `json:"type"`               // "command", "prompt", or "http"
	Command  string            `json:"command,omitempty"`  // type=command: shell command
	Prompt   string            `json:"prompt,omitempty"`   // type=prompt: LLM prompt ($ARGUMENTS = payload)
	URL      string            `json:"url,omitempty"`      // type=http: POST endpoint
	Headers  map[string]string `json:"headers,omitempty"`  // type=http: request headers
	Matcher  string            `json:"matcher,omitempty"`  // tool name filter: exact (case-insensitive) or /regex/
	If       string            `json:"if,omitempty"`       // tool arguments JSON filter: /regex/, or the exact JSON
	Blocking *bool             `json:"blocking,omitempty"` // can block execution
	Timeout  *int              `json:"timeout,omitempty"`  // seconds (default 60)
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
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
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

	Hooks HooksConfig // lifecycle hooks

	MCPServers map[string]MCPServer

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
	if len(s.Hooks) > 0 {
		r.Hooks = s.Hooks
	}
	r.MCPServers = s.MCPServers
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

// SettingsPath returns <cwd>/.codebot/settings.json.
func SettingsPath(cwd string) string {
	return filepath.Join(cwd, ConfigDir, "settings.json")
}

// projectConfigExists reports whether <cwd>/.codebot/settings.json exists.
func projectConfigExists(cwd string) bool {
	_, err := os.Stat(SettingsPath(cwd))
	return err == nil
}

// globalSettingsPath returns ~/.codebot/settings.json.
func globalSettingsPath() string {
	return filepath.Join(UserConfigDir(), "settings.json")
}

// globalConfigExists reports whether ~/.codebot/settings.json exists.
func globalConfigExists() bool {
	_, err := os.Stat(globalSettingsPath())
	return err == nil
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
var settingsWriteMu sync.Mutex

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

// Load merges the global (~/.codebot/settings.json) and project
// (<cwd>/.codebot/settings.json) settings, the project's winning, and applies
// defaults. It fails when a settings file exists and cannot be parsed or holds
// an unsupported value. Model is deliberately never defaulted — hardcoded
// model names go stale; boot validates it is set.
func Load(cwd string) (Resolved, error) {
	var global Settings
	if UserConfigDir() != "" {
		var err error
		if global, err = loadFile(globalSettingsPath()); err != nil {
			return Resolved{}, err
		}
	}
	project, err := loadFile(SettingsPath(cwd))
	if err != nil {
		return Resolved{}, err
	}

	r := mergeSettings(global, project).resolve()
	if err := validateResolved(r); err != nil {
		return Resolved{}, err
	}
	if r.SearchAPIKey == "" {
		r.SearchAPIKey = os.Getenv(strings.ToUpper(r.SearchProvider) + "_API_KEY")
	}
	r.Permissions = normalizePermissionRoots(cwd, r.Permissions)
	return r, nil
}

// mergeSettings merges two Settings; non-nil fields in override take precedence.
func mergeSettings(base, override Settings) Settings {
	if override.Provider != nil {
		base.Provider = override.Provider
	}
	if override.Model != nil {
		base.Model = override.Model
	}
	if len(override.Providers) > 0 {
		if base.Providers == nil {
			base.Providers = make(map[string]*ProviderConfig)
		}
		for k, v := range override.Providers {
			if v == nil {
				continue
			}
			existing, ok := base.Providers[k]
			if !ok || existing == nil {
				base.Providers[k] = v
				continue
			}
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
	if len(override.Hooks) > 0 {
		if base.Hooks == nil {
			base.Hooks = make(HooksConfig)
		}
		for event, entries := range override.Hooks {
			base.Hooks[event] = append(base.Hooks[event], entries...)
		}
	}
	if override.Permissions != nil {
		if base.Permissions == nil {
			base.Permissions = &PermissionsConfig{}
		}
		base.Permissions.Allow = append(base.Permissions.Allow, override.Permissions.Allow...)
		base.Permissions.Deny = append(base.Permissions.Deny, override.Permissions.Deny...)
		base.Permissions.ReadRoots = append(base.Permissions.ReadRoots, override.Permissions.ReadRoots...)
		base.Permissions.WriteRoots = append(base.Permissions.WriteRoots, override.Permissions.WriteRoots...)
	}
	if override.Telemetry != nil {
		base.Telemetry = override.Telemetry
	}
	if override.Snapshot != nil {
		base.Snapshot = override.Snapshot
	}
	for name, server := range override.MCPServers {
		if base.MCPServers == nil {
			base.MCPServers = make(map[string]MCPServer)
		}
		base.MCPServers[name] = server
	}
	return base
}

// patchSettingsFile applies the non-nil fields of patch to the settings file
// at path.
func patchSettingsFile(path string, patch Settings) error {
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()
	existing, err := loadFile(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(mergeSettings(existing, patch), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	return writeFileAtomic(path, data, 0o600)
}

// PatchEffectiveSettings writes to the project settings file when it exists,
// otherwise to the global settings file. Runtime UI changes should use this
// so the visible state matches the settings layer Load reads.
func PatchEffectiveSettings(cwd string, patch Settings) error {
	path := globalSettingsPath()
	if projectConfigExists(cwd) {
		path = SettingsPath(cwd)
	}
	return patchSettingsFile(path, patch)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
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

func loadFile(path string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(path)
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
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = expandHome(root)
		if !filepath.IsAbs(root) {
			root = filepath.Join(cwd, root)
		}
		if root = filepath.Clean(root); !slices.Contains(out, root) {
			out = append(out, root)
		}
	}
	if len(out) == 0 {
		return []string{filepath.Clean(cwd)}
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
