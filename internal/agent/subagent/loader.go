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

// agentFrontmatter is the strict schema for the YAML block at the top of a
// .codebot/agents/*.md file. Every field a user might set must appear here;
// the YAML decoder is configured to reject unknown keys, which means a typo
// like `tooLs:` will fail loud instead of being silently ignored.
//
// Field naming follows YAML conventions (snake_case-ish via tags) rather
// than Go conventions, because users write the YAML by hand.
type agentFrontmatter struct {
	Name            string   `yaml:"name"`
	Description     string   `yaml:"description"`
	Tools           []string `yaml:"tools,omitempty"`
	DisallowedTools []string `yaml:"disallowedTools,omitempty"`
	Model           string   `yaml:"model,omitempty"`
	MaxTurns        int      `yaml:"maxTurns,omitempty"`
}

// LoadDir reads every *.md file under dir and parses them as agent
// definitions. Files that fail to parse are reported but do not abort the
// load — a single broken file should not block the user from using the rest
// of their agent library. The returned errors slice has one entry per
// broken file; the returned definitions slice excludes those files.
//
// A dir that does not exist holds no agents.
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

// LoadFile reads a single agent file end-to-end: file I/O, frontmatter
// extraction, YAML decoding, post-validation.
func LoadFile(path string) (AgentDefinition, error) {
	raw, err := regular.ReadFile(path)
	if err != nil {
		return AgentDefinition{}, err
	}
	// Every agent file declares its metadata in a frontmatter block.
	front, body, ok := frontmatter.Split(string(raw))
	if !ok {
		return AgentDefinition{}, errors.New(`missing YAML frontmatter: the file must open with a "---" line and close the block with another`)
	}

	var fm agentFrontmatter
	dec := yaml.NewDecoder(strings.NewReader(front))
	dec.KnownFields(true) // strict: unknown keys are errors, not silently ignored
	if err := dec.Decode(&fm); err != nil {
		return AgentDefinition{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	// Default the agent name to the filename stem when frontmatter omits
	// it. This lets users write a single-purpose agent file without
	// repeating the name — convention over configuration.
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
