package config

import (
	"fmt"
	"path/filepath"

	"github.com/voocel/codebot/internal/diag"
)

// setup.go — first-run configuration logic. The interactive wizard itself
// lives in the TUI (internal/ui/tui/onboarding.go); this file owns detection
// and persistence so the flow stays testable without a terminal.

// SetupChoice is the input collected by the onboarding wizard.
type SetupChoice struct {
	Provider string // provider key, e.g. "anthropic", or a custom name
	Type     string // protocol type for custom providers; empty = derive from name
	BaseURL  string // optional custom endpoint
	APIKey   string
	Model    string // exact model id; required — hardcoded defaults go stale
}

// SetupOutcome reports what ApplySetup wrote.
type SetupOutcome struct {
	Provider string
	Model    string
	Path     string // settings.json that was written
}

// NeedsSetup reports whether no settings file exists (global or project).
// Credentials come exclusively from settings.json, so a missing file means
// the interactive frontend must run onboarding before booting the runtime.
func NeedsSetup(cwd string) bool {
	return !GlobalConfigExists() && !ProjectConfigExists(cwd)
}

// ApplySetup persists the choice into ~/.codebot/settings.json. It patches
// rather than overwrites, so unrelated fields in an existing file survive a
// re-run (codebot -setup).
func ApplySetup(c SetupChoice) (SetupOutcome, error) {
	if c.Provider == "" {
		return SetupOutcome{}, fmt.Errorf("provider is required: %w", diag.ErrConfig)
	}
	if c.APIKey == "" {
		return SetupOutcome{}, fmt.Errorf("API key is required: %w", diag.ErrConfig)
	}
	if c.Model == "" {
		return SetupOutcome{}, fmt.Errorf("model is required: %w", diag.ErrConfig)
	}
	if _, err := ResolveProviderType(c.Provider, c.Type); err != nil {
		return SetupOutcome{}, err
	}

	pc := &ProviderConfig{APIKey: c.APIKey, Models: []string{c.Model}}
	if c.Type != "" {
		pc.Type = c.Type
	}
	if c.BaseURL != "" {
		pc.BaseURL = c.BaseURL
	}
	patch := Settings{
		Provider:  &c.Provider,
		Model:     &c.Model,
		Providers: map[string]*ProviderConfig{c.Provider: pc},
	}
	if err := PatchGlobalSettings(patch); err != nil {
		return SetupOutcome{}, fmt.Errorf("save settings: %w", err)
	}
	return SetupOutcome{
		Provider: c.Provider,
		Model:    c.Model,
		Path:     filepath.Join(UserConfigDir(), "settings.json"),
	}, nil
}
