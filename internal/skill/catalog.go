package skill

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Catalog is the skills available, by name. It does not change; reloading
// makes a new one.
type Catalog struct {
	list   []Spec // by name
	byName map[string]Spec
}

// NewCatalog collects skills. Of several with one name, the one from the most
// trusted source wins: project, then user, then bundled, then remote; among
// equals, the later one.
func NewCatalog(specs []Spec) *Catalog {
	c := &Catalog{byName: make(map[string]Spec, len(specs))}
	for _, spec := range specs {
		if cur, ok := c.byName[spec.Name]; ok && sourcePriority(cur.Source) < sourcePriority(spec.Source) {
			continue
		}
		c.byName[spec.Name] = spec
	}
	for _, name := range slices.Sorted(maps.Keys(c.byName)) {
		c.list = append(c.list, c.byName[name])
	}
	return c
}

// List returns the skills active in the workspace at cwd: those whose Paths
// match something there, or that have none. An empty cwd lists them all.
func (c *Catalog) List(cwd string) []Spec {
	var out []Spec
	for _, spec := range c.list {
		if skillIsActive(spec, cwd) {
			out = append(out, spec)
		}
	}
	return out
}

// Get returns the named skill if it is active in the workspace at cwd.
func (c *Catalog) Get(name, cwd string) (Spec, bool) {
	spec, ok := c.byName[normalizeName(name)]
	if !ok || !skillIsActive(spec, cwd) {
		return Spec{}, false
	}
	return spec, true
}

// LoadDir loads the skills in dir: each *.md file, and each subdirectory
// holding a SKILL.md, at any depth, named after the subdirectory. What fails
// to load is reported, and left out.
func LoadDir(dir string) ([]Spec, []error) {
	entries, err := os.ReadDir(dir)
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
		spec, err := loadSkillFile(path, strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
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
	if spec, err := loadSkillFile(filepath.Join(dir, "SKILL.md"), name); err == nil {
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

func loadSkillFile(path, name string) (Spec, error) {
	data, err := os.ReadFile(path)
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

type frontmatter struct {
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
	raw, body := splitFrontmatter(content)
	var fm frontmatter
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
