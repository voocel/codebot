package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigDir is the project-level config directory name.
const ConfigDir = ".codebot"

// ProviderConfig holds credentials for a single provider.
type ProviderConfig struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

// Settings holds application-level configuration.
// Fields use pointer types so unset fields fall back to defaults.
type Settings struct {
	Model      *string                    `json:"model,omitempty"`      // "provider/model" e.g. "anthropic/claude-sonnet-4-6"
	SmallModel *string                    `json:"small_model,omitempty"` // "provider/model" for explore sub-agent
	Providers  map[string]*ProviderConfig `json:"providers,omitempty"`

	ContextWindow  *int  `json:"context_window,omitempty"`
	AutoCompaction *bool `json:"auto_compaction,omitempty"`

	ThinkingLevel *string `json:"thinking_level,omitempty"`

	MaxTurns *int `json:"max_turns,omitempty"`

	SearchProvider *string `json:"search_provider,omitempty"`
	SearchAPIKey   *string `json:"search_api_key,omitempty"`
}

// Resolved holds settings resolved to concrete values (no pointers).
type Resolved struct {
	Provider   string                     // active provider name
	Model      string                     // active model name (without provider prefix)
	Providers  map[string]ProviderConfig  // per-provider credentials
	SmallModel string                     // "provider/model" or ""

	ContextWindow  int
	AutoCompaction bool
	ThinkingLevel  string
	MaxTurns       int
	SearchProvider string
	SearchAPIKey   string
}

// providerEnvVars maps provider names to their standard environment variable names.
var providerEnvVars = map[string]struct{ key, base string }{
	"anthropic": {"ANTHROPIC_API_KEY", ""},
	"openai":    {"OPENAI_API_KEY", "OPENAI_BASE_URL"},
	"gemini":    {"GEMINI_API_KEY", ""},
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

// ParseModelID splits "provider/model" into (provider, model).
// If no "/" is present, returns ("", id).
func ParseModelID(id string) (provider, model string) {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		return id[:i], id[i+1:]
	}
	return "", id
}

// FormatModelID combines provider and model into "provider/model".
func FormatModelID(provider, model string) string {
	if provider == "" {
		return model
	}
	return provider + "/" + model
}

// Resolve converts Settings to Resolved using defaults for unset fields.
func (s Settings) Resolve() Resolved {
	r := Resolved{
		Provider:       "openai",
		Providers:      make(map[string]ProviderConfig),
		ContextWindow:  128000,
		AutoCompaction: true,
		ThinkingLevel:  "low",
		MaxTurns:       30,
	}
	if s.Model != nil {
		r.Provider, r.Model = ParseModelID(*s.Model)
	}
	if s.SmallModel != nil {
		r.SmallModel = *s.SmallModel
	}
	for k, v := range s.Providers {
		if v != nil {
			r.Providers[k] = *v
		}
	}
	if s.ContextWindow != nil {
		r.ContextWindow = *s.ContextWindow
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
	return r
}

// SettingsPath returns <cwd>/.codebot/settings.json.
func SettingsPath(cwd string) string {
	return filepath.Join(cwd, ConfigDir, "settings.json")
}

// SessionsDir returns <cwd>/.codebot/sessions/.
func SessionsDir(cwd string) string {
	return filepath.Join(cwd, ConfigDir, "sessions")
}

// PromptsDir returns <cwd>/.codebot/prompts/.
func PromptsDir(cwd string) string {
	return filepath.Join(cwd, ConfigDir, "prompts")
}

// PlansDir returns <cwd>/.codebot/plans/.
func PlansDir(cwd string) string {
	return filepath.Join(cwd, ConfigDir, "plans")
}

// SkillsDir returns <cwd>/.codebot/skills/.
func SkillsDir(cwd string) string {
	return filepath.Join(cwd, ConfigDir, "skills")
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
			base.Providers[k] = v
		}
	}
	if override.ContextWindow != nil {
		base.ContextWindow = override.ContextWindow
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
	return base
}

// SaveSettings writes settings to <cwd>/.codebot/settings.json.
func SaveSettings(cwd string, s Settings) error {
	path := SettingsPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
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

// DefaultModelName returns the default model name for a given provider.
func DefaultModelName(prov string) string {
	switch prov {
	case "anthropic":
		return "claude-sonnet-4-5"
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

	// Normalize search provider name.
	switch strings.ToLower(strings.TrimSpace(settings.SearchProvider)) {
	case "jina", "jina.ai", "jinaai":
		settings.SearchProvider = "jina"
	}

	return settings
}
