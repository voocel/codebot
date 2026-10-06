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

// Consents are what the user agreed to run as them: of each project, by its
// root, what of its surface; of each plugin, by its source as fetched, what
// it runs and a git one's commit. They are kept in the user's home, where
// no project can agree for them, and written as the user agrees alone:
// reading, fetching and loading agree to nothing.
type Consents struct {
	Projects map[string]Consent `json:"projects,omitempty"`
	Plugins  map[string]Consent `json:"plugins,omitempty"`
}

// Consent is what the user decided of a project or a plugin.
type Consent struct {
	// Denied says the user does not trust the project: none of its surface
	// is in effect, nor are they asked about it.
	Denied bool `json:"denied,omitempty"`
	// Commit is the commit of a git plugin they agreed to.
	Commit string `json:"commit,omitempty"`
	// Surface is what they agreed to run, and Declined what they would not:
	// they are not asked about it again.
	Surface  Surface `json:"surface,omitempty"`
	Declined Surface `json:"declined,omitempty"`
}

// Standing returns where the user stands on surface, as c tells. A consent
// to all of it, for a session, is Consent{Surface: surface}.
func (c Consent) Standing(surface Surface) Standing {
	if c.Denied {
		return Standing{Surface: surface, Declined: surface}
	}
	return Standing{Surface: surface, Agreed: surface.Intersect(c.Surface), Declined: surface.Intersect(c.Declined)}
}

// Decided returns c with what the user decided of shown, of surface: they
// agreed to agreed and declined the rest of shown. What else of surface
// they decided on stands; what is no longer on it is forgotten.
func (c Consent) Decided(surface, shown, agreed Surface) Consent {
	declined := shown.Missing(agreed)
	return Consent{
		Commit:   c.Commit,
		Surface:  surface.Intersect(c.Surface).Missing(declined).With(agreed...),
		Declined: surface.Intersect(c.Declined).Missing(agreed).With(declined...),
	}
}

func consentsPath() string { return filepath.Join(config.UserConfigDir(), "consent.json") }

// ReadConsents returns what the user agreed to.
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

// EditConsents applies edit to what the user agreed to.
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
		c.Plugins = map[string]Consent{}
	}
	edit(&c)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(consentsPath(), data, 0o600)
}
