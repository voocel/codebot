package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ConfigDir is the project-level config directory name.
const ConfigDir = ".codebot"

// KnownProviderTypes maps well-known provider names to their protocol type.
var KnownProviderTypes = map[string]string{
	"anthropic":  "anthropic",
	"openai":     "openai",
	"openrouter": "openrouter",
	"gemini":     "gemini",
}

// ProviderConfig holds credentials and model configuration for a single provider.
type ProviderConfig struct {
	Type       string   `json:"type,omitempty"` // protocol: openai/anthropic/gemini; inferred from name if empty
	APIKey     string   `json:"api_key,omitempty"`
	BaseURL    string   `json:"base_url,omitempty"`
	Models     []string `json:"models,omitempty"`      // available model list for this provider
	SmallModel string   `json:"small_model,omitempty"` // lightweight model for sub-agents
}

// ProviderType returns the protocol type for this provider.
// Uses explicit Type field first, then infers from the provider name,
// defaulting to "openai" for unknown providers.
func (pc ProviderConfig) ProviderType(name string) string {
	if pc.Type != "" {
		return pc.Type
	}
	if t, ok := KnownProviderTypes[name]; ok {
		return t
	}
	return "openai"
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
	Provider   *string                    `json:"provider,omitempty"`    // provider name (matches key in providers map)
	Model      *string                    `json:"model,omitempty"`       // model name sent to API as-is
	SmallModel *string                    `json:"small_model,omitempty"` // sub-agent model; defaults to Model if empty
	Providers  map[string]*ProviderConfig `json:"providers,omitempty"`

	ContextWindow *int `json:"context_window,omitempty"`

	AutoCompaction *bool `json:"auto_compaction,omitempty"`

	ThinkingLevel *string `json:"thinking_level,omitempty"`

	MaxTurns *int `json:"max_turns,omitempty"`

	SearchProvider *string `json:"search_provider,omitempty"`
	SearchAPIKey   *string `json:"search_api_key,omitempty"`

	Hooks HooksConfig `json:"hooks,omitempty"` // lifecycle hooks

	Permissions *PermissionsConfig `json:"permissions,omitempty"`
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

	ContextWindow  int // auto-detected from model registry at boot
	AutoCompaction bool
	ThinkingLevel  string
	MaxTurns       int
	SearchProvider string
	SearchAPIKey   string

	Hooks HooksConfig // lifecycle hooks

	Permissions PermissionsConfig // user-defined permission rules
}

// providerEnvVars maps provider names to their standard environment variable names.
var providerEnvVars = map[string]struct{ key, base string }{
	"anthropic":  {"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"},
	"openai":     {"OPENAI_API_KEY", "OPENAI_BASE_URL"},
	"openrouter": {"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL"},
	"gemini":     {"GEMINI_API_KEY", "GEMINI_BASE_URL"},
}

// ProviderCredentials returns API key and base URL for the given provider.
// It checks the providers map first, then falls back to standard environment variables.
// Config base URL is preserved even when the API key comes from the environment.
func (r Resolved) ProviderCredentials(prov string) (apiKey, baseURL string) {
	pc, hasConfig := r.Providers[prov]
	if hasConfig && pc.APIKey != "" {
		return pc.APIKey, pc.BaseURL
	}
	apiKey, baseURL = EnvCredentials(prov)
	if hasConfig && baseURL == "" {
		baseURL = pc.BaseURL
	}
	return apiKey, baseURL
}

// ProviderEnvKey returns the standard environment variable name for a provider's API key.
func ProviderEnvKey(prov string) string {
	if ev, ok := providerEnvVars[prov]; ok {
		return ev.key
	}
	return strings.ToUpper(prov) + "_API_KEY"
}

// EnvCredentials returns API key and base URL from standard environment variables
// for the given provider (e.g. ANTHROPIC_API_KEY, OPENAI_API_KEY).
func EnvCredentials(prov string) (apiKey, baseURL string) {
	envVars, ok := providerEnvVars[prov]
	if !ok {
		return "", ""
	}
	apiKey = os.Getenv(envVars.key)
	if envVars.base != "" {
		baseURL = os.Getenv(envVars.base)
	}
	return apiKey, baseURL
}

// DetectEnvProvider scans known providers for available environment variable credentials.
// Returns the provider name and env var key of the first match, or empty if none found.
func DetectEnvProvider() (provider, envKey string) {
	order := []string{"anthropic", "openai", "gemini", "openrouter"}
	for _, prov := range order {
		if key, _ := EnvCredentials(prov); key != "" {
			return prov, providerEnvVars[prov].key
		}
	}
	return "", ""
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
		Provider:       "openai",
		Providers:      make(map[string]ProviderConfig),
		AutoCompaction: true,
		ThinkingLevel:  "low",
		MaxTurns:       200,
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
	if s.ContextWindow != nil {
		r.ContextWindow = *s.ContextWindow
	}
	for k, v := range s.Providers {
		if v != nil {
			r.Providers[k] = *v
		}
	}
	if s.AutoCompaction != nil {
		r.AutoCompaction = *s.AutoCompaction
	}
	if s.ThinkingLevel != nil {
		r.ThinkingLevel = *s.ThinkingLevel
	}
	if s.MaxTurns != nil {
		r.MaxTurns = *s.MaxTurns
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
	return r
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

// CommandsDir returns <cwd>/.codebot/commands/.
func CommandsDir(cwd string) string {
	return filepath.Join(cwd, ConfigDir, "commands")
}

// PlansDir returns ~/.codebot/plans/<projectID>/.
func PlansDir(cwd string) string {
	return filepath.Join(UserConfigDir(), "plans", projectID(cwd))
}

// ApprovalsPath returns ~/.codebot/approvals/<projectID>.json.
func ApprovalsPath(cwd string) string {
	return filepath.Join(UserConfigDir(), "approvals", projectID(cwd)+".json")
}

// TasksDir returns ~/.codebot/tasks/.
func TasksDir() string {
	return filepath.Join(UserConfigDir(), "tasks")
}

// SkillsDir returns <cwd>/.codebot/skills/.
func SkillsDir(cwd string) string {
	return filepath.Join(cwd, ConfigDir, "skills")
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

// LoadSettings loads and merges settings from global (~/.codebot/settings.json)
// and project (<cwd>/.codebot/settings.json). Project-level values override global.
func LoadSettings(cwd string) Resolved {
	var global Settings
	if dir := UserConfigDir(); dir != "" {
		global = loadSettingsFile(filepath.Join(dir, "settings.json"))
	}
	project := loadSettingsFile(SettingsPath(cwd))
	return mergeSettings(global, project).Resolve()
}

// LoadSettingsStrict loads and merges settings like LoadSettings, but returns
// an error when either settings file exists and cannot be parsed.
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

	return mergeSettings(global, project).Resolve(), nil
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
	if override.ContextWindow != nil {
		base.ContextWindow = override.ContextWindow
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
			base.Providers[k] = existing
		}
	}
	if override.AutoCompaction != nil {
		base.AutoCompaction = override.AutoCompaction
	}
	if override.ThinkingLevel != nil {
		base.ThinkingLevel = override.ThinkingLevel
	}
	if override.MaxTurns != nil {
		base.MaxTurns = override.MaxTurns
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
	return base
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
	return os.WriteFile(path, data, 0o600)
}

// PatchGlobalSettings loads the global settings, applies the patch, and saves back.
// Only non-nil fields in patch are updated.
func PatchGlobalSettings(patch Settings) error {
	dir := UserConfigDir()
	if dir == "" {
		return fmt.Errorf("cannot determine user config directory")
	}
	existing, err := loadSettingsFileStrict(filepath.Join(dir, "settings.json"))
	if err != nil {
		return err
	}
	merged := mergeSettings(existing, patch)
	return SaveSettings(merged)
}

// PatchProjectSettings loads project-level settings, applies the patch, and saves back.
func PatchProjectSettings(cwd string, patch Settings) error {
	path := SettingsPath(cwd)
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
	return os.WriteFile(path, data, 0o600)
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
		return s, fmt.Errorf("malformed %s: %w", path, err)
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
	default:
		return "gpt-5-mini"
	}
}

// ResolveAll merges global and project settings, applies defaults,
// and returns a fully resolved configuration.
func ResolveAll(cwd string) Resolved {
	settings := LoadSettings(cwd)

	// Model: settings > default per provider
	if settings.Model == "" {
		settings.Model = DefaultModelName(settings.Provider)
	}

	// SmallModel: settings > provider config > main model.
	if settings.SmallModel == "" {
		if pc, ok := settings.Providers[settings.Provider]; ok && pc.SmallModel != "" {
			settings.SmallModel = pc.SmallModel
		} else {
			settings.SmallModel = settings.Model
		}
	}

	// Normalize search provider name.
	switch strings.ToLower(strings.TrimSpace(settings.SearchProvider)) {
	case "jina", "jina.ai", "jinaai":
		settings.SearchProvider = "jina"
	}
	if settings.SearchAPIKey == "" {
		settings.SearchAPIKey = searchAPIKeyFromEnv(settings.SearchProvider)
	}

	settings.Permissions = normalizePermissionRoots(cwd, settings.Permissions)

	return settings
}

// ResolveAllStrict resolves settings like ResolveAll, but refuses to continue
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
