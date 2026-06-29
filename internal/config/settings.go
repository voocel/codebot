package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/voocel/codebot/internal/diag"
	"github.com/voocel/codebot/internal/provider"
)

// ConfigDir is the project-level config directory name.
const ConfigDir = ".codebot"

// ProviderConfig holds credentials and model configuration for a single provider.
type ProviderConfig struct {
	Type       string         `json:"type,omitempty"` // LiteLLM protocol type; required only when the provider name is not a known litellm provider
	APIKey     string         `json:"api_key,omitempty"`
	BaseURL    string         `json:"base_url,omitempty"`
	Models     []string       `json:"models,omitempty"`      // available model list for this provider
	SmallModel string         `json:"small_model,omitempty"` // lightweight model for sub-agents
	Extra      map[string]any `json:"extra,omitempty"`       // provider-level litellm config: headers, user_agent, anthropic_beta
}

// TelemetryConfig configures OpenTelemetry trace export to an OTLP backend
// (e.g. Langfuse). Telemetry stays off unless Enabled is true.
type TelemetryConfig struct {
	Enabled   bool   `json:"enabled,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`   // OTLP/HTTP endpoint URL, e.g. https://cloud.langfuse.com/api/public/otel
	PublicKey string `json:"public_key,omitempty"` // basic-auth username
	SecretKey string `json:"secret_key,omitempty"` // basic-auth password
}

// ProviderType resolves the protocol type for this provider.
// The protocol type maps to a name registered in litellm's provider registry.
func (pc ProviderConfig) ProviderType(name string) (string, error) {
	return ResolveProviderType(name, pc.Type)
}

// ResolveProviderType resolves a provider's protocol type. When explicitType
// is set it wins (and must be registered); otherwise the provider name itself
// must be a registered litellm provider.
func ResolveProviderType(name, explicitType string) (string, error) {
	provType := strings.ToLower(strings.TrimSpace(explicitType))
	if provType != "" {
		if provider.IsSupportedType(provType) {
			return provType, nil
		}
		return "", fmt.Errorf("configuration error: providers.%s.type=%q is unsupported: %w", name, explicitType, diag.ErrConfig)
	}
	lowered := strings.ToLower(strings.TrimSpace(name))
	if provider.IsSupportedType(lowered) {
		return lowered, nil
	}
	return "", fmt.Errorf("configuration error: providers.%s.type is required for custom providers: %w", name, diag.ErrConfig)
}

// ResolveConfiguredProviderType resolves the protocol type for a configured provider.
func ResolveConfiguredProviderType(providers map[string]ProviderConfig, name string) (string, error) {
	if pc, ok := providers[name]; ok {
		return pc.ProviderType(name)
	}
	return ResolveProviderType(name, "")
}

// HookEntry describes a single hook.
// Supported types: "command" (shell), "prompt" (LLM evaluation), "http" (POST).
type HookEntry struct {
	Type     string            `json:"type"`               // "command", "prompt", or "http"
	Command  string            `json:"command,omitempty"`  // type=command: shell command
	Prompt   string            `json:"prompt,omitempty"`   // type=prompt: LLM prompt ($ARGUMENTS = payload)
	URL      string            `json:"url,omitempty"`      // type=http: POST endpoint
	Headers  map[string]string `json:"headers,omitempty"`  // type=http: request headers
	Matcher  string            `json:"matcher,omitempty"`  // tool name filter: exact or /regex/
	If       string            `json:"if,omitempty"`       // argument content filter: substring or /regex/
	Blocking *bool             `json:"blocking,omitempty"` // can block execution
	Timeout  *int              `json:"timeout,omitempty"`  // seconds (default 60)
}

// HooksConfig maps event names to their hook entries.
type HooksConfig map[string][]HookEntry

// Settings holds application-level configuration.
// Fields use pointer types so unset fields fall back to defaults.
type Settings struct {
	Provider        *string                    `json:"provider,omitempty"`         // provider name (matches key in providers map)
	Model           *string                    `json:"model,omitempty"`            // model name sent to API as-is
	ReasoningEffort *string                    `json:"reasoning_effort,omitempty"` // "" = provider default; off | low | medium | high | xhigh | max
	SmallModel      *string                    `json:"small_model,omitempty"`      // sub-agent model; defaults to Model if empty
	Providers       map[string]*ProviderConfig `json:"providers,omitempty"`

	MaxTurns *int `json:"max_turns,omitempty"`

	// CompactWindow caps the effective context window used for compaction.
	// Effective = min(model's detected window, CompactWindow). 0 = disabled.
	CompactWindow *int `json:"compact_window,omitempty"`
	// CompactRatio triggers compaction when usage >= effective * ratio.
	// Range (0, 1). 0 = engine default (fixed headroom buffer).
	CompactRatio *float64 `json:"compact_ratio,omitempty"`

	SearchProvider *string `json:"search_provider,omitempty"`
	SearchAPIKey   *string `json:"search_api_key,omitempty"`

	Hooks HooksConfig `json:"hooks,omitempty"` // lifecycle hooks

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
	Provider   string                    // active provider name
	Model      string                    // model name sent to API as-is
	SmallModel string                    // sub-agent model; equals Model when not configured
	Providers  map[string]ProviderConfig // per-provider credentials

	ContextWindow   int     // effective window after applying CompactWindow cap
	CompactWindow   int     // user-configured cap on effective window; 0 = disabled
	CompactRatio    float64 // usage ratio that triggers compaction; 0 = engine default
	ReasoningEffort string
	MaxTurns        int
	SearchProvider  string
	SearchAPIKey    string

	Hooks HooksConfig // lifecycle hooks

	Permissions PermissionsConfig // user-defined permission rules

	Telemetry TelemetryConfig // OTLP trace export config

	Snapshot bool // workspace checkpoints for /undo; defaults on
}

// ProviderCredentials returns API key and base URL for the given provider.
// It checks the providers map first, then falls back to standard environment variables.
func (r Resolved) ProviderCredentials(prov string) (apiKey, baseURL string) {
	if pc, ok := r.Providers[prov]; ok && pc.APIKey != "" {
		return pc.APIKey, pc.BaseURL
	}
	return EnvCredentials(prov)
}

// ProviderExtra returns provider-level litellm config for the given provider.
func (r Resolved) ProviderExtra(prov string) map[string]any {
	if pc, ok := r.Providers[prov]; ok {
		return pc.Extra
	}
	return nil
}

// ProviderEnvKey derives the standard env var name for a provider's API key
// (e.g. "openai" → "OPENAI_API_KEY"). All provider env conventions follow the
// same UPPER(name)_API_KEY / UPPER(name)_BASE_URL pattern.
func ProviderEnvKey(prov string) string {
	return providerEnvVar(prov, "API_KEY")
}

// EnvCredentials reads the API key and base URL from the standard env vars
// derived from the provider name.
func EnvCredentials(prov string) (apiKey, baseURL string) {
	prov = strings.TrimSpace(prov)
	if prov == "" {
		return "", ""
	}
	apiKey = os.Getenv(providerEnvVar(prov, "API_KEY"))
	baseURL = os.Getenv(providerEnvVar(prov, "BASE_URL"))
	return apiKey, baseURL
}

// DetectEnvProvider scans every registered litellm provider for available env
// credentials. Returns the first match's provider name and env var key.
func DetectEnvProvider() (providerName, envKey string) {
	for _, prov := range provider.SupportedTypeNames() {
		if key, _ := EnvCredentials(prov); key != "" {
			return prov, providerEnvVar(prov, "API_KEY")
		}
	}
	return "", ""
}

// providerEnvVar returns UPPER(name)_SUFFIX, with non-alphanumeric chars in
// the provider name collapsed to underscores so names like "open-router" map
// cleanly to "OPEN_ROUTER_API_KEY".
func providerEnvVar(prov, suffix string) string {
	var b strings.Builder
	b.Grow(len(prov) + 1 + len(suffix))
	for _, r := range strings.ToUpper(strings.TrimSpace(prov)) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	b.WriteByte('_')
	b.WriteString(suffix)
	return b.String()
}

// FormatModelID combines provider and model into "provider/model".
// If model already contains "/", it is returned as-is.
func FormatModelID(provider, model string) string {
	if provider == "" || strings.Contains(model, "/") {
		return model
	}
	return provider + "/" + model
}

// Resolve converts Settings to Resolved using defaults for unset fields.
func (s Settings) Resolve() Resolved {
	r := Resolved{
		Provider:  "openai",
		Providers: make(map[string]ProviderConfig),
		MaxTurns:  200,
		Snapshot:  true,
	}
	if s.Provider != nil && *s.Provider != "" {
		r.Provider = *s.Provider
	}
	if s.Model != nil {
		r.Model = *s.Model
	}
	if s.SmallModel != nil {
		r.SmallModel = *s.SmallModel
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
	if s.CompactWindow != nil && *s.CompactWindow > 0 {
		r.CompactWindow = *s.CompactWindow
	}
	if s.CompactRatio != nil {
		ratio := *s.CompactRatio
		if ratio > 0 && ratio < 1 {
			r.CompactRatio = ratio
		}
	}
	if s.SearchProvider != nil {
		r.SearchProvider = *s.SearchProvider
	}
	if s.SearchAPIKey != nil {
		r.SearchAPIKey = *s.SearchAPIKey
	}
	if len(s.Hooks) > 0 {
		r.Hooks = s.Hooks
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

// ValidateResolved rejects unsupported values after global/project settings
// have been merged and defaults applied.
func ValidateResolved(r Resolved) error {
	if _, ok := provider.ResolveThinkingLevel(nil, r.ReasoningEffort); !ok {
		return fmt.Errorf("configuration error: reasoning_effort=%q is unsupported; use empty string, off, low, medium, high, xhigh, or max: %w", r.ReasoningEffort, diag.ErrConfig)
	}
	return nil
}

// SettingsPath returns <cwd>/.codebot/settings.json.
func SettingsPath(cwd string) string {
	return filepath.Join(cwd, ConfigDir, "settings.json")
}

// ProjectConfigExists reports whether <cwd>/.codebot/settings.json exists.
func ProjectConfigExists(cwd string) bool {
	_, err := os.Stat(SettingsPath(cwd))
	return err == nil
}

// GlobalSettingsPath returns ~/.codebot/settings.json.
func GlobalSettingsPath() string {
	return filepath.Join(UserConfigDir(), "settings.json")
}

// GlobalConfigExists reports whether ~/.codebot/settings.json exists.
func GlobalConfigExists() bool {
	_, err := os.Stat(GlobalSettingsPath())
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

// SessionMemoryPath returns ~/.codebot/projects/<projectID>/session-memory.md.
// This file is project-scoped (shared across sessions in the same cwd) so
// that resuming or starting a new session can inherit accumulated context.
func SessionMemoryPath(cwd string) string {
	return filepath.Join(SessionsDir(cwd), "session-memory.md")
}

// CommandsDir returns <cwd>/.codebot/commands/.
func CommandsDir(cwd string) string {
	return filepath.Join(cwd, ConfigDir, "commands")
}

// PlansDir returns ~/.codebot/plans/. A single global directory shared across
// projects; word-slug filenames make collisions vanishingly rare.
func PlansDir(_ string) string {
	return filepath.Join(UserConfigDir(), "plans")
}

// ApprovalsPath returns ~/.codebot/approvals/<projectID>.json.
func ApprovalsPath(cwd string) string {
	return filepath.Join(UserConfigDir(), "approvals", projectID(cwd)+".json")
}

// TasksDir returns ~/.codebot/tasks/.
func TasksDir() string {
	return filepath.Join(UserConfigDir(), "tasks")
}

// TeamDir returns ~/.codebot/tasks/<sessionID>/team/ — the per-session home
// for team coordination artifacts (roster, teammate transcripts, mailbox
// backlog) that must survive a restart alongside the durable task list. It
// lives under the session's task dir so a session's entire coordination
// state is reclaimed together.
func TeamDir(sessionID string) string {
	return filepath.Join(TasksDir(), sessionID, "team")
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

// LoadSettingsStrict loads and merges settings from global (~/.codebot/settings.json)
// and project (<cwd>/.codebot/settings.json). Project-level values override
// global. Returns an error when either settings file exists and cannot be parsed.
func LoadSettingsStrict(cwd string) (Resolved, error) {
	var global Settings
	if dir := UserConfigDir(); dir != "" {
		var err error
		global, err = loadSettingsFileStrict(filepath.Join(dir, "settings.json"))
		if err != nil {
			return Resolved{}, err
		}
	}

	project, err := loadSettingsFileStrict(SettingsPath(cwd))
	if err != nil {
		return Resolved{}, err
	}

	resolved := mergeSettings(global, project).Resolve()
	if err := ValidateResolved(resolved); err != nil {
		return Resolved{}, err
	}
	return resolved, nil
}

// mergeSettings merges two Settings; non-nil fields in override take precedence.
func mergeSettings(base, override Settings) Settings {
	if override.Provider != nil {
		base.Provider = override.Provider
	}
	if override.Model != nil {
		base.Model = override.Model
	}
	if override.SmallModel != nil {
		base.SmallModel = override.SmallModel
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
			if len(v.Extra) > 0 {
				existing.Extra = cloneExtra(v.Extra)
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
	return base
}

func cloneExtra(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// SaveSettings writes settings to ~/.codebot/settings.json (global).
func SaveSettings(s Settings) error {
	dir := UserConfigDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()
	return writeFileAtomic(path, data, 0o600)
}

// PatchGlobalSettings loads the global settings, applies the patch, and saves back.
// Only non-nil fields in patch are updated.
func PatchGlobalSettings(patch Settings) error {
	dir := UserConfigDir()
	if dir == "" {
		return fmt.Errorf("cannot determine user config directory: %w", diag.ErrConfig)
	}
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()
	existing, err := loadSettingsFileStrict(filepath.Join(dir, "settings.json"))
	if err != nil {
		return err
	}
	merged := mergeSettings(existing, patch)
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	return writeFileAtomic(filepath.Join(dir, "settings.json"), data, 0o600)
}

// PatchProjectSettings loads project-level settings, applies the patch, and saves back.
func PatchProjectSettings(cwd string, patch Settings) error {
	path := SettingsPath(cwd)
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()
	existing, err := loadSettingsFileStrict(path)
	if err != nil {
		return err
	}
	merged := mergeSettings(existing, patch)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create project config dir: %w", err)
	}
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	return writeFileAtomic(path, data, 0o600)
}

// PatchEffectiveSettings writes to the project settings file when it exists,
// otherwise to the global settings file. Runtime UI changes should use this
// so the visible state matches the settings layer ResolveAllStrict reads.
func PatchEffectiveSettings(cwd string, patch Settings) error {
	if ProjectConfigExists(cwd) {
		return PatchProjectSettings(cwd, patch)
	}
	return PatchGlobalSettings(patch)
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

func loadSettingsFile(path string) Settings {
	var s Settings
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		fmt.Fprintf(os.Stderr, "warning: malformed %s: %v\n", path, err)
	}
	return s
}

func loadSettingsFileStrict(path string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("configuration error: malformed settings.json (%s): %w: %w", path, diag.ErrConfig, err)
	}
	return s, nil
}

// DefaultModelName returns the default model name for a given provider.
func DefaultModelName(prov string) string {
	switch prov {
	case "anthropic":
		return "claude-sonnet-4-6"
	case "gemini":
		return "gemini-3.0-flash"
	case "deepseek":
		return "deepseek-chat"
	default:
		return "gpt-5-mini"
	}
}

// ResolveAllStrict merges global and project settings, applies defaults, and
// returns a fully resolved configuration. Refuses to continue
// when an existing settings file is malformed.
func ResolveAllStrict(cwd string) (Resolved, error) {
	settings, err := LoadSettingsStrict(cwd)
	if err != nil {
		return Resolved{}, err
	}

	if settings.Model == "" {
		settings.Model = DefaultModelName(settings.Provider)
	}

	if settings.SmallModel == "" {
		if pc, ok := settings.Providers[settings.Provider]; ok && pc.SmallModel != "" {
			settings.SmallModel = pc.SmallModel
		} else {
			settings.SmallModel = settings.Model
		}
	}

	switch strings.ToLower(strings.TrimSpace(settings.SearchProvider)) {
	case "jina", "jina.ai", "jinaai":
		settings.SearchProvider = "jina"
	}
	if settings.SearchAPIKey == "" {
		settings.SearchAPIKey = searchAPIKeyFromEnv(settings.SearchProvider)
	}

	settings.Permissions = normalizePermissionRoots(cwd, settings.Permissions)
	return settings, nil
}

func searchAPIKeyFromEnv(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "tavily":
		return os.Getenv("TAVILY_API_KEY")
	case "jina", "jina.ai", "jinaai":
		return os.Getenv("JINA_API_KEY")
	default:
		return os.Getenv("SEARCH_API_KEY")
	}
}

func normalizePermissionRoots(cwd string, perms PermissionsConfig) PermissionsConfig {
	perms.ReadRoots = normalizeRootList(cwd, perms.ReadRoots, true)
	perms.WriteRoots = normalizeRootList(cwd, perms.WriteRoots, true)
	perms.ReadRoots = unionRoots(perms.ReadRoots, perms.WriteRoots)
	return perms
}

func normalizeRootList(cwd string, roots []string, defaultToCWD bool) []string {
	if len(roots) == 0 {
		if !defaultToCWD {
			return nil
		}
		roots = []string{"."}
	}

	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}

		root = ExpandHome(root)
		if !filepath.IsAbs(root) {
			root = filepath.Join(cwd, root)
		}
		root = filepath.Clean(root)
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}

	if len(out) == 0 && defaultToCWD {
		return []string{filepath.Clean(cwd)}
	}
	return out
}

func ExpandHome(path string) string {
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

func unionRoots(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, root := range base {
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	for _, root := range extra {
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	return out
}
