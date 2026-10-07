package extension

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/voocel/codebot/internal/infra/config"
)

// Consents records what the user agreed to run, per project root and per
// plugin source (plugin.Source.ID). It lives in the user's home so no project
// can grant itself consent, and only an explicit user decision writes it:
// reading, fetching or loading never does.
type Consents struct {
	Projects map[string]Consent       `json:"projects,omitempty"`
	Plugins  map[string]PluginConsent `json:"plugins,omitempty"`
}

// Consent is the user's decision on a project, item by item.
type Consent struct {
	// Denied means the user distrusts the project and is not asked again.
	Denied bool `json:"denied,omitempty"`
	// Declined items are not asked about again.
	Surface  Surface `json:"surface,omitempty"`
	Declined Surface `json:"declined,omitempty"`
}

// PluginConsent is the user's agreement to a plugin as a whole: its author
// tested it as one, so it runs all of Surface or nothing.
type PluginConsent struct {
	// Commit pins a git plugin to the commit the user agreed to.
	Commit  string  `json:"commit,omitempty"`
	Surface Surface `json:"surface,omitempty"`
}

func (c Consent) Standing(surface Surface) Standing {
	if c.Denied {
		return Standing{Surface: surface}
	}
	return Standing{Surface: surface, Agreed: surface.Intersect(c.Surface), Declined: surface.Intersect(c.Declined)}
}

// Decided records a decision on the shown items: agreed is accepted and the
// rest of shown is declined. Earlier decisions on items still on surface
// stand; items no longer on surface are forgotten.
func (c Consent) Decided(surface, shown, agreed Surface) Consent {
	declined := shown.Missing(agreed)
	return Consent{
		Surface:  surface.Intersect(c.Surface).Missing(declined).With(agreed...),
		Declined: surface.Intersect(c.Declined).Missing(agreed).With(declined...),
	}
}

func consentsPath() string { return filepath.Join(config.UserConfigDir(), "consent.json") }

func ReadConsents() (Consents, error) {
	var c Consents
	data, err := os.ReadFile(consentsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", consentsPath(), err)
	}
	return c, nil
}

func EditConsents(edit func(*Consents)) error {
	if err := os.MkdirAll(config.UserConfigDir(), 0o755); err != nil {
		return err
	}
	unlock, err := config.LockFile(consentsPath())
	if err != nil {
		return err
	}
	defer unlock()
	c, err := ReadConsents()
	if err != nil {
		return err
	}
	if c.Projects == nil {
		c.Projects = map[string]Consent{}
	}
	if c.Plugins == nil {
		c.Plugins = map[string]PluginConsent{}
	}
	edit(&c)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(consentsPath(), data, 0o600)
}
