package config

import (
	"errors"
	"fmt"
	"os"
)

type SetupChoice struct {
	Provider string // a known provider such as "anthropic", or a custom name
	Type     string // protocol type for custom providers; "" = derive from name
	BaseURL  string
	APIKey   string
	Model    string // required: hardcoded defaults go stale
}

type SetupOutcome struct {
	Provider string
	Model    string
	Path     string
}

// NeedsSetup reports whether the user settings file is missing. Credentials
// come only from it, never from a project, so onboarding must run first.
func NeedsSetup() bool {
	_, err := os.Stat(UserSettingsPath())
	return err != nil
}

// ApplySetup patches the user settings rather than overwriting them, so a
// re-run (codebot -setup) keeps unrelated fields.
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
