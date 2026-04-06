package skill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Catalog struct {
	mu        sync.RWMutex
	cwd       string
	baseSpecs []Spec
	extraDirs []DirSource
	list      []Spec
	byName    map[string]Spec
}

type DirSource struct {
	Path   string
	Source string
}

type frontmatter struct {
	Name                   string   `yaml:"name"`
	Description            string   `yaml:"description"`
	WhenToUse              string   `yaml:"when_to_use"`
	Version                string   `yaml:"version"`
	ArgumentHint           string   `yaml:"argument-hint"`
	Arguments              []string `yaml:"arguments"`
	Context                string   `yaml:"context"`
	Agent                  string   `yaml:"agent"`
	Model                  string   `yaml:"model"`
	Effort                 string   `yaml:"effort"`
	AllowedTools           any      `yaml:"allowed-tools"`
	Paths                  []string `yaml:"paths"`
	UserInvocable          *bool    `yaml:"user-invocable"`
	DisableModelInvocation *bool    `yaml:"disable-model-invocation"`
}

func NewCatalog(cwd string, baseSpecs []Spec, extraDirs ...DirSource) *Catalog {
	c := &Catalog{
		cwd:       cwd,
		baseSpecs: append([]Spec(nil), baseSpecs...),
		extraDirs: append([]DirSource(nil), extraDirs...),
	}
	c.Reload()
	return c
}

func NewStaticCatalog(specs []Spec) *Catalog {
	c := &Catalog{}
	c.setSpecs(specs)
	return c
}

func LoadFromDir(dir, source string) []Spec {
	return loadSkillsFromDir(dir, source)
}

func ValidateDir(dir, source string) ([]Spec, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{err}
	}

	var specs []Spec
	var errs []error
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			spec, ok := findSkillInDir(path, entry.Name(), source)
			if ok {
				specs = append(specs, spec)
				continue
			}
			errs = append(errs, fmt.Errorf("%s: no valid skill found", path))
			continue
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		spec, err := loadSkillFile(path, source)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		specs = append(specs, spec)
	}
	return specs, errs
}

func (c *Catalog) Reload() {
	specs := append([]Spec(nil), c.baseSpecs...)
	for _, extra := range c.extraDirs {
		if strings.TrimSpace(extra.Path) == "" {
			continue
		}
		source := strings.TrimSpace(extra.Source)
		if source == "" {
			source = "plugin"
		}
		specs = append(specs, loadSkillsFromDir(extra.Path, source)...)
	}
	c.setSpecs(deduplicateSpecs(specs))
}

func deduplicateSpecs(specs []Spec) []Spec {
	if len(specs) < 2 {
		return append([]Spec(nil), specs...)
	}
	deduped := make([]Spec, 0, len(specs))
	byName := make(map[string]int, len(specs))
	byIdentity := make(map[string]int, len(specs))
	for _, spec := range specs {
		nameKey := NormalizeName(spec.Name)
		identityKey := specIdentityKey(spec)
		if idx, ok := byName[nameKey]; ok {
			releaseSpecKeys(deduped[idx], byName, byIdentity, idx)
			deduped[idx] = spec
			storeSpecKeys(spec, byName, byIdentity, idx, identityKey)
			continue
		}
		if identityKey != "" {
			if idx, ok := byIdentity[identityKey]; ok {
				releaseSpecKeys(deduped[idx], byName, byIdentity, idx)
				deduped[idx] = spec
				storeSpecKeys(spec, byName, byIdentity, idx, identityKey)
				continue
			}
		}
		idx := len(deduped)
		deduped = append(deduped, spec)
		storeSpecKeys(spec, byName, byIdentity, idx, identityKey)
	}
	sort.Slice(deduped, func(i, j int) bool { return deduped[i].Name < deduped[j].Name })
	return deduped
}

func storeSpecKeys(spec Spec, byName map[string]int, byIdentity map[string]int, idx int, identityKey string) {
	byName[NormalizeName(spec.Name)] = idx
	if identityKey != "" {
		byIdentity[identityKey] = idx
	}
}

func releaseSpecKeys(spec Spec, byName map[string]int, byIdentity map[string]int, idx int) {
	nameKey := NormalizeName(spec.Name)
	if cur, ok := byName[nameKey]; ok && cur == idx {
		delete(byName, nameKey)
	}
	if identityKey := specIdentityKey(spec); identityKey != "" {
		if cur, ok := byIdentity[identityKey]; ok && cur == idx {
			delete(byIdentity, identityKey)
		}
	}
}

func specIdentityKey(spec Spec) string {
	if spec.FilePath == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(spec.FilePath)
	if err == nil && resolved != "" {
		return filepath.Clean(resolved)
	}
	abs, err := filepath.Abs(spec.FilePath)
	if err != nil {
		return filepath.Clean(spec.FilePath)
	}
	return filepath.Clean(abs)
}

func (c *Catalog) setSpecs(specs []Spec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = make([]Spec, len(specs))
	c.byName = make(map[string]Spec, len(specs))
	for i, spec := range specs {
		spec = cloneSpec(spec)
		if spec.GetPrompt == nil && spec.FilePath != "" {
			spec.GetPrompt = buildPromptFn(spec, "")
		}
		c.list[i] = spec
		c.byName[spec.Name] = spec
	}
}

func (c *Catalog) List() []Spec {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Spec, 0, len(c.list))
	for _, spec := range c.list {
		if !skillIsActive(spec, c.cwd) {
			continue
		}
		out = append(out, cloneSpec(spec))
	}
	return out
}

func (c *Catalog) Get(name string) (Spec, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	spec, ok := c.byName[NormalizeName(name)]
	if !ok || !skillIsActive(spec, c.cwd) {
		return Spec{}, false
	}
	return cloneSpec(spec), true
}

func loadSkillsFromDir(dir, source string) []Spec {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var specs []Spec
	for _, entry := range entries {
		if entry.IsDir() {
			if spec, ok := findSkillInDir(filepath.Join(dir, entry.Name()), entry.Name(), source); ok {
				specs = append(specs, spec)
			}
			continue
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		spec, err := loadSkillFile(filepath.Join(dir, entry.Name()), source)
		if err == nil {
			specs = append(specs, spec)
		}
	}
	return specs
}

func findSkillInDir(dir, dirName, source string) (Spec, bool) {
	skillFile := filepath.Join(dir, "SKILL.md")
	if spec, err := loadSkillFile(skillFile, source); err == nil {
		if spec.Name == "skill" {
			spec.Name = NormalizeName(dirName)
		}
		if ValidName(spec.Name) {
			return spec, true
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return Spec{}, false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if spec, ok := findSkillInDir(filepath.Join(dir, entry.Name()), dirName, source); ok {
			return spec, true
		}
	}
	return Spec{}, false
}

func loadSkillFile(path, source string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	return parseSkillContent(string(data), skillSource{
		NameHint: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		FilePath: path,
		BaseDir:  filepath.Dir(path),
		Source:   source,
	}, buildPromptFn)
}

type skillSource struct {
	NameHint string
	FilePath string
	BaseDir  string
	Source   string
}

func parseSkillContent(content string, src skillSource, promptFactory func(Spec, string) GetPromptFn) (Spec, error) {
	fm, keys, err := parseFrontmatter(content)
	if err != nil {
		return Spec{}, err
	}

	name := src.NameHint
	if fm.Name != "" {
		name = fm.Name
	}
	name = NormalizeName(name)
	if !ValidName(name) {
		return Spec{}, os.ErrInvalid
	}

	userInvocable := true
	if fm.UserInvocable != nil {
		userInvocable = *fm.UserInvocable
	}
	disableModel := false
	if fm.DisableModelInvocation != nil {
		disableModel = *fm.DisableModelInvocation
	}

	body := strings.TrimSpace(StripFrontmatter(content))
	description := strings.TrimSpace(fm.Description)
	if description == "" {
		description = FirstLine(body, 80)
	}

	spec := Spec{
		Name:                   name,
		Description:            description,
		WhenToUse:              strings.TrimSpace(fm.WhenToUse),
		Version:                strings.TrimSpace(fm.Version),
		FilePath:               src.FilePath,
		BaseDir:                src.BaseDir,
		Source:                 src.Source,
		DisableModelInvocation: disableModel,
		DisableUserInvocation:  !userInvocable,
		ArgumentHint:           strings.TrimSpace(fm.ArgumentHint),
		ArgumentNames:          append([]string(nil), fm.Arguments...),
		Context:                normalizeContext(fm.Context),
		Agent:                  strings.TrimSpace(fm.Agent),
		Model:                  strings.TrimSpace(fm.Model),
		Effort:                 strings.TrimSpace(fm.Effort),
		AllowedTools:           normalizeAllowedTools(fm.AllowedTools),
		Paths:                  append([]string(nil), fm.Paths...),
		HasExplicitDescription: strings.TrimSpace(fm.Description) != "",
		FrontmatterKeys:        keys,
	}
	spec.GetPrompt = promptFactory(spec, content)
	return spec, nil
}

func parseFrontmatter(content string) (frontmatter, []string, error) {
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return frontmatter{}, nil, nil
	}
	rest := content[4:]
	raw, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return frontmatter{}, nil, nil
	}

	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		return frontmatter{}, nil, err
	}
	var keys []string
	if len(node.Content) > 0 && node.Content[0].Kind == yaml.MappingNode {
		mapping := node.Content[0]
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			keys = append(keys, mapping.Content[i].Value)
		}
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(raw), &fm); err != nil {
		return frontmatter{}, nil, err
	}
	return fm, keys, nil
}

func buildPromptFn(spec Spec, _ string) GetPromptFn {
	return func(ctx context.Context, args string, sessionID string) (string, error) {
		_ = ctx
		data, err := os.ReadFile(spec.FilePath)
		if err != nil {
			return "", err
		}
		body := strings.TrimSpace(StripFrontmatter(string(data)))
		body = ExpandVars(body, spec.BaseDir, sessionID)
		if SourceAllowsShellExecution(spec.Source) {
			body = ExpandShellInjections(body)
		}
		body = ExpandArgs(body, args)
		return WrapPrompt(spec, body), nil
	}
}

func buildStaticPromptFn(spec Spec, content string) GetPromptFn {
	return func(ctx context.Context, args string, sessionID string) (string, error) {
		_ = ctx
		body := strings.TrimSpace(StripFrontmatter(content))
		body = ExpandVars(body, spec.BaseDir, sessionID)
		if SourceAllowsShellExecution(spec.Source) {
			body = ExpandShellInjections(body)
		}
		body = ExpandArgs(body, args)
		return WrapPrompt(spec, body), nil
	}
}

func normalizeAllowedTools(v any) []string {
	switch raw := v.(type) {
	case string:
		var out []string
		for _, item := range strings.Split(raw, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		var out []string
		for _, item := range raw {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case []string:
		return append([]string(nil), raw...)
	default:
		return nil
	}
}

func normalizeContext(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "fork") {
		return "fork"
	}
	return "inline"
}

func cloneSpec(spec Spec) Spec {
	spec.ArgumentNames = append([]string(nil), spec.ArgumentNames...)
	spec.AllowedTools = append([]string(nil), spec.AllowedTools...)
	spec.Paths = append([]string(nil), spec.Paths...)
	spec.FrontmatterKeys = append([]string(nil), spec.FrontmatterKeys...)
	return spec
}
