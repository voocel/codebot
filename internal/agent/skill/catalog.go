package skill

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/voocel/codebot/internal/lib/frontmatter"
	"github.com/voocel/codebot/internal/lib/regular"
)

// Catalog is the skills available, by name. It does not change; reloading
// makes a new one.
type Catalog struct {
	list   []Spec // by name
	byName map[string]Spec
}

// NewCatalog collects skills. Of several with one name the first wins: the
// caller lists them by precedence.
func NewCatalog(specs []Spec) *Catalog {
	c := &Catalog{byName: make(map[string]Spec, len(specs))}
	for _, spec := range specs {
		if _, ok := c.byName[spec.Name]; !ok {
			c.byName[spec.Name] = spec
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.byName)) {
		c.list = append(c.list, c.byName[name])
	}
	return c
}

// List returns the skills, by name.
func (c *Catalog) List() []Spec { return slices.Clone(c.list) }

// Get returns the named skill.
func (c *Catalog) Get(name string) (Spec, bool) {
	spec, ok := c.byName[normalizeName(name)]
	return spec, ok
}

// Active returns the catalog of the skills active in the workspace at cwd:
// those whose Paths match something there, or that have none. Matching may
// walk the workspace, so it happens here, once: a conversation keeps the
// result with the rest of what it tells the model about its workspace, and
// offers the user and the model the same skills.
func (c *Catalog) Active(cwd string) *Catalog {
	var specs []Spec
	for _, spec := range c.list {
		if skillIsActive(spec, cwd) {
			specs = append(specs, spec)
		}
	}
	return NewCatalog(specs)
}

// LoadDir loads the skills in dir: each *.md file, and each subdirectory
// holding a SKILL.md, at any depth, named after the subdirectory. What fails
// to load is reported, and left out. A dir that does not exist holds no
// skills.
func LoadDir(dir string) ([]Spec, []error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{err}
	}

	var specs []Spec
	var errs []error
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			if spec, ok := findSkillInDir(path, entry.Name()); ok {
				specs = append(specs, spec)
			} else {
				errs = append(errs, fmt.Errorf("%s: no valid skill found", path))
			}
			continue
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		spec, err := LoadFile(path, strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		specs = append(specs, spec)
	}
	return specs, errs
}

// findSkillInDir finds the SKILL.md in dir or, failing that, the first one
// below it.
func findSkillInDir(dir, name string) (Spec, bool) {
	if spec, err := LoadFile(filepath.Join(dir, "SKILL.md"), name); err == nil {
		return spec, true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Spec{}, false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if spec, ok := findSkillInDir(filepath.Join(dir, entry.Name()), name); ok {
			return spec, true
		}
	}
	return Spec{}, false
}

// LoadFile loads the skill file at path, named name unless it names
// itself.
func LoadFile(path, name string) (Spec, error) {
	data, err := regular.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	spec, err := parseSkill(string(data), name)
	if err != nil {
		return Spec{}, err
	}
	spec.FilePath, spec.BaseDir = path, filepath.Dir(path)
	return spec, nil
}

type skillFrontmatter struct {
	Name                   string   `yaml:"name"`
	Description            string   `yaml:"description"`
	WhenToUse              string   `yaml:"when_to_use"`
	ArgumentHint           string   `yaml:"argument-hint"`
	Context                string   `yaml:"context"`
	Agent                  string   `yaml:"agent"`
	Model                  string   `yaml:"model"`
	AllowedTools           any      `yaml:"allowed-tools"`
	Paths                  []string `yaml:"paths"`
	UserInvocable          *bool    `yaml:"user-invocable"`
	DisableModelInvocation bool     `yaml:"disable-model-invocation"`
}

// parseSkill reads a skill file's frontmatter. The skill is named by its
// frontmatter, else by name.
func parseSkill(content, name string) (Spec, error) {
	raw, body, _ := frontmatter.Split(content)
	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(raw), &fm); err != nil {
		return Spec{}, err
	}
	if fm.Name != "" {
		name = fm.Name
	}
	if !ValidName(name) {
		return Spec{}, fmt.Errorf("invalid skill name %q", name)
	}

	description := strings.TrimSpace(fm.Description)
	if description == "" {
		description = firstLine(body, 80)
	}
	mode := "inline"
	if strings.EqualFold(strings.TrimSpace(fm.Context), "fork") {
		mode = "fork"
	}
	return Spec{
		Name:                   normalizeName(name),
		Description:            description,
		WhenToUse:              strings.TrimSpace(fm.WhenToUse),
		DisableModelInvocation: fm.DisableModelInvocation,
		DisableUserInvocation:  fm.UserInvocable != nil && !*fm.UserInvocable,
		ArgumentHint:           strings.TrimSpace(fm.ArgumentHint),
		Context:                mode,
		Agent:                  strings.TrimSpace(fm.Agent),
		Model:                  strings.TrimSpace(fm.Model),
		AllowedTools:           allowedTools(fm.AllowedTools),
		Paths:                  fm.Paths,
	}, nil
}

// allowedTools reads allowed-tools, a comma-separated string or a list.
func allowedTools(v any) []string {
	var items []string
	switch raw := v.(type) {
	case string:
		items = strings.Split(raw, ",")
	case []any:
		for _, item := range raw {
			if s, ok := item.(string); ok {
				items = append(items, s)
			}
		}
	}
	var out []string
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
