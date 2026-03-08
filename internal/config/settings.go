package config

import (
	"crypto/sha256"
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
	Provider   *string                    `json:"provider,omitempty"`    // provider name (matches key in providers map)
	Model      *string                    `json:"model,omitempty"`       // model name sent to API as-is
	SmallModel *string                    `json:"small_model,omitempty"` // "provider/model" for explore sub-agent
	Providers  map[string]*ProviderConfig `json:"providers,omitempty"`

	AutoCompaction *bool `json:"auto_compaction,omitempty"`

	ThinkingLevel *string `json:"thinking_level,omitempty"`

	MaxTurns *int `json:"max_turns,omitempty"`

	SearchProvider *string `json:"search_provider,omitempty"`
	SearchAPIKey   *string `json:"search_api_key,omitempty"`

	AllowedCommands []string `json:"allowed_commands,omitempty"` // project-level always-allow list for dangerous commands
}

// Resolved holds settings resolved to concrete values (no pointers).
type Resolved struct {
	Provider   string                    // active provider name
	Model      string                    // model name sent to API as-is
	Providers  map[string]ProviderConfig // per-provider credentials
	SmallModel string                    // "provider/model" or ""

	ContextWindow  int // auto-detected from model registry at boot
	AutoCompaction bool
	ThinkingLevel  string
	MaxTurns       int
	SearchProvider string
	SearchAPIKey   string

	AllowedCommands []string // project-level always-allow list for dangerous commands
}

// providerEnvVars maps provider names to their standard environment variable names.
var providerEnvVars = map[string]struct{ key, base string }{
	"anthropic":  {"ANTHROPIC_API_KEY", ""},
	"openai":     {"OPENAI_API_KEY", "OPENAI_BASE_URL"},
	"openrouter": {"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL"},
	"gemini":     {"GEMINI_API_KEY", ""},
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
		MaxTurns:       30,
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
	return r
}

// SettingsPath returns <cwd>/.codebot/settings.json.
func SettingsPath(cwd string) string {
	return filepath.Join(cwd, ConfigDir, "settings.json")
}

// SessionsDir returns ~/.codebot/sessions/<projectID>/.
// Sessions are stored globally but scoped by project.
func SessionsDir(cwd string) string {
	return filepath.Join(UserConfigDir(), "sessions", projectID(cwd))
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

// projectID returns a stable short directory name for a project path.
// Format: <dirname>-<sha256_first_8_hex>.
func projectID(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	h := sha256.Sum256([]byte(abs))
	return fmt.Sprintf("%s-%x", filepath.Base(abs), h[:4])
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
			base.Providers[k] = v
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
