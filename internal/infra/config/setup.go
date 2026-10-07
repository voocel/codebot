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
	typ, conn, err := c.dial()
	if err != nil {
		return err
	}
	return provider.Check(typ, conn)
}

// Models lists the models the choice's key reaches: those codebot knows
// first, as some lists hold embedding, speech and image models too, and each
// part newest first where the vendor dates them. Listing checks the key: a
// rejected one fails with litellm.ErrorTypeAuth.
func (c SetupChoice) Models(ctx context.Context) ([]SetupModel, error) {
	typ, conn, err := c.dial()
	if err != nil {
		return nil, err
	}
	listed, err := provider.ListModels(ctx, typ, conn)
	if err != nil {
		return nil, err
	}
	facts := provider.NewModels()
	var known, rest []SetupModel
	for _, m := range listed {
		f, ok := facts.Lookup(provider.ModelSpec{Provider: c.Provider, Type: typ, Model: m.ID})
		sm := SetupModel{ID: m.ID, Name: m.Name, Window: f.MaxInputTokens}
		if ok {
			known = append(known, sm)
		} else {
			rest = append(rest, sm)
		}
	}
	return append(known, rest...), nil
}

// dial returns the protocol and the connection the choice makes, named as
// its provider is.
func (c SetupChoice) dial() (string, llmprovider.Config, error) {
	typ, err := resolveProviderType(c.Provider, c.Type)
	if err != nil {
		return "", llmprovider.Config{}, err
	}
	s, err := UserSettings()
	if err != nil {
		return "", llmprovider.Config{}, err
	}
	conn := c.entry(s, typ).connection()
	conn.Name = c.Provider
	return typ, conn, nil
}

// entry is the provider's settings as the setup leaves them. The connection
// is the choice's alone, so the setup saves what it checked; the rest of the
// entry, such as extra headers and the models, stays.
func (c SetupChoice) entry(s Settings, typ string) ProviderConfig {
	var pc ProviderConfig
	if old := s.Providers[c.Provider]; old != nil {
		pc = *old
	}
	pc.Type, pc.BaseURL, pc.APIKey = c.Type, c.BaseURL, c.APIKey
	if typ != "openai" {
		pc.API = "" // only the OpenAI protocol takes one
	}
	return pc
}

// ApplySetup makes the choice the default model. It edits the user settings
// rather than overwriting them, so a re-run (codebot -setup) keeps the other
// providers, and the provider's other models.
func ApplySetup(c SetupChoice) error {
	if c.Provider == "" || c.Model == "" {
		return errors.New("a provider and a model are required")
	}
	typ, conn, err := c.dial()
	if err != nil {
		return err
	}
	if err := provider.Check(typ, conn); err != nil {
		return err
	}
	err = EditUserSettings(func(s *Settings) {
		pc := c.entry(*s, typ)
		if !slices.Contains(pc.Models, c.Model) {
			pc.Models = append(pc.Models, c.Model)
		}
		if s.Providers == nil {
			s.Providers = map[string]*ProviderConfig{}
		}
		s.Providers[c.Provider] = &pc
		// The effort picked for the old model may not suit the new one.
		s.Provider, s.Model, s.ReasoningEffort = &c.Provider, &c.Model, nil
	})
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}
