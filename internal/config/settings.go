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

	SearchProvider *string `json:"search_provider,omitempty"`
	SearchAPIKey   *string `json:"search_api_key,omitempty"`

	SmallModel *string `json:"small_model,omitempty"` // lightweight model for explore sub-agent (e.g. "haiku", "gpt-4o-mini")
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
	SearchProvider  string
	SearchAPIKey    string
	SmallModel    string // lightweight model for explore sub-agent (e.g. "haiku", "gpt-4o-mini")
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
	if s.SearchProvider != nil {
		r.SearchProvider = *s.SearchProvider
	}
	if s.SearchAPIKey != nil {
		r.SearchAPIKey = *s.SearchAPIKey
	}
	if s.SmallModel != nil {
		r.SmallModel = *s.SmallModel
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
	if override.DefaultProvider != nil {
		base.DefaultProvider = override.DefaultProvider
	}
	if override.DefaultModel != nil {
		base.DefaultModel = override.DefaultModel
	}
	if override.APIKey != nil {
		base.APIKey = override.APIKey
	}
	if override.BaseURL != nil {
		base.BaseURL = override.BaseURL
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
	if override.ShowTokens != nil {
		base.ShowTokens = override.ShowTokens
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
	if override.SmallModel != nil {
		base.SmallModel = override.SmallModel
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
	if settings.DefaultModel == "" {
		settings.DefaultModel = DefaultModelName(settings.DefaultProvider)
	}

	// Normalize search provider name.
	switch strings.ToLower(strings.TrimSpace(settings.SearchProvider)) {
	case "jina", "jina.ai", "jinaai":
		settings.SearchProvider = "jina"
	}

	return settings
}
