package config

import (
	"errors"
	"fmt"
	"os"
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

// NeedsSetup reports whether the user has no settings file. Credentials come
// from it alone, never from a project's, so without it the interactive
// frontend runs onboarding before booting the runtime.
func NeedsSetup() bool {
	_, err := os.Stat(UserSettingsPath())
	return err != nil
}

// ApplySetup persists the choice into ~/.codebot/settings.json. It patches
// rather than overwrites, so unrelated fields in an existing file survive a
// re-run (codebot -setup).
func ApplySetup(c SetupChoice) (SetupOutcome, error) {
	if c.Provider == "" {
		return SetupOutcome{}, errors.New("provider is required")
	}
	if c.APIKey == "" {
		return SetupOutcome{}, errors.New("API key is required")
	}
	if c.Model == "" {
		return SetupOutcome{}, errors.New("model is required")
	}
	if _, err := resolveProviderType(c.Provider, c.Type); err != nil {
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
	if err := PatchUserSettings(patch); err != nil {
		return SetupOutcome{}, fmt.Errorf("save settings: %w", err)
	}
	return SetupOutcome{
		Provider: c.Provider,
		Model:    c.Model,
		Path:     UserSettingsPath(),
	}, nil
}
