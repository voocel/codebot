package subagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/voocel/codebot/internal/lib/frontmatter"
	"github.com/voocel/codebot/internal/lib/regular"
)

type agentFrontmatter struct {
	Name            string   `yaml:"name"`
	Description     string   `yaml:"description"`
	Tools           []string `yaml:"tools,omitempty"`
	DisallowedTools []string `yaml:"disallowedTools,omitempty"`
	Model           string   `yaml:"model,omitempty"`
	MaxTurns        int      `yaml:"maxTurns,omitempty"`
}

// LoadDir reports broken files and skips them, so one bad file doesn't hide
// the rest. A missing dir holds no agents.
func LoadDir(dir string) (defs []AgentDefinition, errs []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("read agents dir %s: %w", dir, err)}
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(dir, name)
		def, err := LoadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		defs = append(defs, def)
	}
	return defs, errs
}

func LoadFile(path string) (AgentDefinition, error) {
	raw, err := regular.ReadFile(path)
	if err != nil {
		return AgentDefinition{}, err
	}
	front, body, ok := frontmatter.Split(string(raw))
	if !ok {
		return AgentDefinition{}, errors.New(`missing YAML frontmatter: the file must open with a "---" line and close the block with another`)
	}

	var fm agentFrontmatter
	dec := yaml.NewDecoder(strings.NewReader(front))
	dec.KnownFields(true) // a typo like `tooLs:` fails instead of being ignored
	if err := dec.Decode(&fm); err != nil {
		return AgentDefinition{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	name := fm.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	def := AgentDefinition{
		Name:            name,
		Description:     fm.Description,
		SystemPrompt:    strings.TrimSpace(body),
		Tools:           fm.Tools,
		DisallowedTools: fm.DisallowedTools,
		Model:           fm.Model,
		MaxTurns:        fm.MaxTurns,
		Origin:          path,
	}
	if err := def.Validate(); err != nil {
		return AgentDefinition{}, err
	}
	return def, nil
}
