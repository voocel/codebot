package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ConfigDir is the project-level config directory name.
const ConfigDir = ".codebot"

// Settings holds application-level configuration.
// Fields use pointer types so unset fields fall back to defaults.
type Settings struct {
	DefaultProvider *string `json:"default_provider,omitempty"`
	DefaultModel    *string `json:"default_model,omitempty"`
	APIKey          *string `json:"api_key,omitempty"`
	BaseURL         *string `json:"base_url,omitempty"`

	ContextWindow  *int  `json:"context_window,omitempty"`
	AutoCompaction *bool `json:"auto_compaction,omitempty"`

	ThinkingLevel *string `json:"thinking_level,omitempty"`
	ShowTokens    *bool   `json:"show_tokens,omitempty"`

	MaxTurns *int `json:"max_turns,omitempty"`
}

// Resolved holds settings resolved to concrete values (no pointers).
type Resolved struct {
	DefaultProvider string
	DefaultModel    string
	APIKey          string
	BaseURL         string
	ContextWindow   int
	AutoCompaction  bool
	ThinkingLevel   string
	ShowTokens      bool
	MaxTurns        int
}

// Resolve converts Settings to Resolved using defaults for unset fields.
func (s Settings) Resolve() Resolved {
	r := Resolved{
		DefaultProvider: "openai",
		ContextWindow:   128000,
		AutoCompaction:  true,
		ThinkingLevel:   "low",
		ShowTokens:      true,
		MaxTurns:        30,
	}
	if s.DefaultProvider != nil {
		r.DefaultProvider = *s.DefaultProvider
	}
	if s.DefaultModel != nil {
		r.DefaultModel = *s.DefaultModel
	}
	if s.APIKey != nil {
		r.APIKey = *s.APIKey
	}
	if s.BaseURL != nil {
		r.BaseURL = *s.BaseURL
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
	if s.ShowTokens != nil {
		r.ShowTokens = *s.ShowTokens
	}
	if s.MaxTurns != nil {
		r.MaxTurns = *s.MaxTurns
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

// LoadSettings loads settings from <cwd>/.codebot/settings.json.
func LoadSettings(cwd string) Resolved {
	s := loadSettingsFile(SettingsPath(cwd))
	return s.Resolve()
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
	json.Unmarshal(data, &s)
	return s
}

// Flags holds CLI flag values for configuration resolution.
type Flags struct {
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
}

// EnvKeyName returns the environment variable name for the API key of the given provider.
func EnvKeyName(prov string) string {
	switch prov {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	default:
		return "OPENAI_API_KEY"
	}
}

// BaseURLEnvName returns the environment variable name for the base URL of the given provider.
func BaseURLEnvName(prov string) string {
	switch prov {
	case "anthropic":
		return "ANTHROPIC_BASE_URL"
	case "gemini":
		return "GEMINI_BASE_URL"
	default:
		return "OPENAI_BASE_URL"
	}
}

// DefaultModelName returns the default model name for a given provider.
func DefaultModelName(prov string) string {
	switch prov {
	case "anthropic":
		return "claude-sonnet-4-5"
	case "gemini":
		return "gemini-2.5-flash"
	default:
		return "gpt-4.1-mini"
	}
}

// ResolveAll merges flags, settings file, and environment variables
// into a single resolved configuration. Precedence: flags > settings > env.
func ResolveAll(cwd string, flags Flags) Resolved {
	settings := LoadSettings(cwd)

	// Provider: flag > settings
	if flags.Provider != "" {
		settings.DefaultProvider = flags.Provider
	}

	// API key: flag > settings > env
	if flags.APIKey != "" {
		settings.APIKey = flags.APIKey
	}
	if settings.APIKey == "" {
		settings.APIKey = os.Getenv(EnvKeyName(settings.DefaultProvider))
	}

	// Base URL: flag > settings > env
	if flags.BaseURL != "" {
		settings.BaseURL = flags.BaseURL
	}
	if settings.BaseURL == "" {
		settings.BaseURL = os.Getenv(BaseURLEnvName(settings.DefaultProvider))
	}

	// Model: flag > settings > default per provider
	if flags.Model != "" {
		settings.DefaultModel = flags.Model
	}
	if settings.DefaultModel == "" {
		settings.DefaultModel = DefaultModelName(settings.DefaultProvider)
	}

	return settings
}
