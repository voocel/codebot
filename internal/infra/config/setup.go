package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	llmprovider "github.com/voocel/litellm/provider"

	"github.com/voocel/codebot/internal/infra/provider"
)

// SetupChoice is a provider connected by the setup.
type SetupChoice struct {
	Provider string // a known provider such as "anthropic", or a custom name
	Type     string // protocol type for custom providers; "" = derive from name
	BaseURL  string
	APIKey   string // "" for a provider that takes none
	Model    string
}

// NeedsSetup reports whether the user settings file is missing. Credentials
// come only from it, never from a project, so onboarding must run first.
func NeedsSetup() bool {
	_, err := os.Stat(UserSettingsPath())
	return err != nil
}

// SetupModel is a model the setup offers.
type SetupModel struct {
	ID, Name string
	Window   int // context window; 0 when unknown
}

// Check reports a choice its provider can't be built from, such as one
// without the key or base URL it needs; it makes no request.
func (c SetupChoice) Check() error {
	typ, err := resolveProviderType(c.Provider, c.Type)
	if err != nil {
		return err
	}
	return provider.Check(typ, c.connection())
}

// Models lists the models the choice's key reaches, newest first where the
// vendor dates them. Listing checks the key: a rejected one fails with
// litellm.ErrorTypeAuth.
func (c SetupChoice) Models(ctx context.Context) ([]SetupModel, error) {
	typ, err := resolveProviderType(c.Provider, c.Type)
	if err != nil {
		return nil, err
	}
	listed, err := provider.ListModels(ctx, typ, c.connection())
	if err != nil {
		return nil, err
	}
	facts := provider.NewModels()
	out := make([]SetupModel, len(listed))
	for i, m := range listed {
		f, _ := facts.Lookup(provider.ModelSpec{Provider: c.Provider, Type: typ, Model: m.ID})
		out[i] = SetupModel{ID: m.ID, Name: m.Name, Window: f.MaxInputTokens}
	}
	return out, nil
}

func (c SetupChoice) connection() llmprovider.Config {
	return ProviderConfig{APIKey: c.APIKey, BaseURL: c.BaseURL}.connection()
}

// ApplySetup makes the choice the default model. It edits the user settings
// rather than overwriting them, so a re-run (codebot -setup) keeps the other
// providers, and the provider's other models.
func ApplySetup(c SetupChoice) error {
	if c.Provider == "" || c.Model == "" {
		return errors.New("a provider and a model are required")
	}
	if err := c.Check(); err != nil {
		return err
	}
	err := EditUserSettings(func(s *Settings) {
		s.Provider, s.Model = &c.Provider, &c.Model
		if s.Providers == nil {
			s.Providers = map[string]*ProviderConfig{}
		}
		pc := s.Providers[c.Provider]
		if pc == nil {
			pc = &ProviderConfig{}
			s.Providers[c.Provider] = pc
		}
		if c.Type != "" {
			pc.Type = c.Type
		}
		if c.BaseURL != "" {
			pc.BaseURL = c.BaseURL
		}
		if c.APIKey != "" {
			pc.APIKey = c.APIKey
		}
		if !slices.Contains(pc.Models, c.Model) {
			pc.Models = append(pc.Models, c.Model)
		}
	})
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}
