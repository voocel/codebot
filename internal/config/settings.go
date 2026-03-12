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

// HookEntry describes a single hook command.
type HookEntry struct {
	Type     string `json:"type"`               // "command" (only type for now)
	Command  string `json:"command"`            // shell command to execute
	Matcher  string `json:"matcher,omitempty"`  // tool name filter: exact or /regex/
	Blocking *bool  `json:"blocking,omitempty"` // PreToolUse: can block execution
	Timeout  *int   `json:"timeout,omitempty"`  // seconds (default 60)
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

	AutoCompaction *bool `json:"auto_compaction,omitempty"`

	ThinkingLevel *string `json:"thinking_level,omitempty"`

	MaxTurns *int `json:"max_turns,omitempty"`

	SearchProvider *string `json:"search_provider,omitempty"`
	SearchAPIKey   *string `json:"search_api_key,omitempty"`

	AllowedCommands []string `json:"allowed_commands,omitempty"` // project-level always-allow list for dangerous commands

	Hooks HooksConfig `json:"hooks,omitempty"` // lifecycle hooks
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

	AllowedCommands []string // project-level always-allow list for dangerous commands

	Hooks HooksConfig // lifecycle hooks
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
func (r Resolved) ProviderCredentials(prov string) (apiKey, baseURL string) {
	if pc, ok := r.Providers[prov]; ok && pc.APIKey != "" {
		return pc.APIKey, pc.BaseURL
	}
	return EnvCredentials(prov)
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
	if len(s.AllowedCommands) > 0 {
		r.AllowedCommands = s.AllowedCommands
	}
	if len(s.Hooks) > 0 {
		r.Hooks = s.Hooks
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
	if len(override.AllowedCommands) > 0 {
		base.AllowedCommands = override.AllowedCommands
	}
	if len(override.Hooks) > 0 {
		if base.Hooks == nil {
			base.Hooks = make(HooksConfig)
		}
		for event, entries := range override.Hooks {
			base.Hooks[event] = append(base.Hooks[event], entries...)
		}
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
	existing := loadSettingsFile(filepath.Join(dir, "settings.json"))
	merged := mergeSettings(existing, patch)
	return SaveSettings(merged)
}

// PatchProjectSettings loads project-level settings, applies the patch, and saves back.
func PatchProjectSettings(cwd string, patch Settings) error {
	path := SettingsPath(cwd)
	existing := loadSettingsFile(path)
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

// AddAllowedCommand appends a command to the project-level allowed_commands list (deduplicated).
func AddAllowedCommand(cwd, cmd string) error {
	existing := loadSettingsFile(SettingsPath(cwd))
	for _, c := range existing.AllowedCommands {
		if c == cmd {
			return nil // already present
		}
	}
	existing.AllowedCommands = append(existing.AllowedCommands, cmd)
	return PatchProjectSettings(cwd, Settings{AllowedCommands: existing.AllowedCommands})
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
	// Persist to config so the user can see and edit it.
	if settings.SmallModel == "" {
		if pc, ok := settings.Providers[settings.Provider]; ok && pc.SmallModel != "" {
			settings.SmallModel = pc.SmallModel
		} else {
			settings.SmallModel = settings.Model
		}
		_ = PatchGlobalSettings(Settings{SmallModel: &settings.SmallModel})
	}

	// Normalize search provider name.
	switch strings.ToLower(strings.TrimSpace(settings.SearchProvider)) {
	case "jina", "jina.ai", "jinaai":
		settings.SearchProvider = "jina"
	}

	return settings
}
