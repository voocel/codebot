package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatterDelim is the canonical marker for YAML frontmatter in markdown
// files. Both the opening and closing lines are this exact string on its own
// line — no flexibility on delimiters, because the grammar is shared with
// every other tool that touches these files (editors, plugins, GitHub).
const frontmatterDelim = "---"

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
	Background      bool     `yaml:"background,omitempty"`
	Isolation       string   `yaml:"isolation,omitempty"`
}

// LoadAgentsDir reads every *.md file under dir and parses them as agent
// definitions. Files that fail to parse are reported but do not abort the
// load — a single broken file should not block the user from using the rest
// of their agent library. The returned errors slice has one entry per
// broken file; the returned definitions slice excludes those files.
//
// dir is allowed to not exist (returns nil, nil) — the loader is happy to
// be called speculatively against directories that haven't been created yet.
func LoadAgentsDir(dir string, source AgentSource) (defs []AgentDefinition, errs []error) {
	if dir == "" {
		return nil, nil
	}
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
		def, err := loadAgentFile(path, source, dir, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		defs = append(defs, def)
	}
	return defs, errs
}

// loadAgentFile reads a single agent file end-to-end: file I/O, frontmatter
// extraction, YAML decoding, post-validation. The function is split out from
// LoadAgentsDir so unit tests can target it without a temp directory.
func loadAgentFile(path string, source AgentSource, baseDir, filename string) (AgentDefinition, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return AgentDefinition{}, err
	}
	front, body, err := splitFrontmatter(raw)
	if err != nil {
		return AgentDefinition{}, err
	}

	var fm agentFrontmatter
	dec := yaml.NewDecoder(bytes.NewReader(front))
	dec.KnownFields(true) // strict: unknown keys are errors, not silently ignored
	if err := dec.Decode(&fm); err != nil {
		return AgentDefinition{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	// Default the agent name to the filename stem when frontmatter omits
	// it. This lets users write a single-purpose agent file without
	// repeating the name — convention over configuration.
	name := fm.Name
	if name == "" {
		name = strings.TrimSuffix(filename, ".md")
	}

	def := AgentDefinition{
		Name:            name,
		Description:     fm.Description,
		SystemPrompt:    strings.TrimSpace(string(body)),
		Tools:           fm.Tools,
		DisallowedTools: fm.DisallowedTools,
		Model:           fm.Model,
		MaxTurns:        fm.MaxTurns,
		Background:      fm.Background,
		Isolation:       fm.Isolation,
		Source:          source,
		BaseDir:         baseDir,
		Filename:        filename,
	}
	if err := def.Validate(); err != nil {
		return AgentDefinition{}, err
	}
	return def, nil
}

// splitFrontmatter pulls the YAML preamble out of a markdown file. Returns
// the (frontmatterBytes, bodyBytes) pair. Files without a frontmatter block
// are an error — every agent file MUST declare its metadata explicitly so
// that name/description are never inferred from filenames alone.
//
// The parser is line-oriented and accepts only the canonical "---\n…---\n"
// shape. We deliberately don't handle BOM, CRLF-only files, or fenced
// markdown — those are tooling problems and a forgiving parser would mask
// real bugs in the user's editor pipeline.
func splitFrontmatter(raw []byte) (front, body []byte, err error) {
	// Accept either "---\n" or "---\r\n" as the leading delimiter. Anything
	// else means no frontmatter block.
	trimmed := raw
	switch {
	case bytes.HasPrefix(trimmed, []byte(frontmatterDelim+"\n")):
		trimmed = trimmed[len(frontmatterDelim)+1:]
	case bytes.HasPrefix(trimmed, []byte(frontmatterDelim+"\r\n")):
		trimmed = trimmed[len(frontmatterDelim)+2:]
	default:
		return nil, nil, fmt.Errorf("missing YAML frontmatter block (file must start with %q)", frontmatterDelim)
	}

	// Find the closing delimiter on its own line. Search line-by-line so a
	// literal "---" inside the YAML body (rare but legal) doesn't confuse us.
	end := indexLine(trimmed, frontmatterDelim)
	if end < 0 {
		return nil, nil, fmt.Errorf("frontmatter block not closed (missing trailing %q line)", frontmatterDelim)
	}
	return trimmed[:end], trimmed[end+len(frontmatterDelim):], nil
}

// indexLine returns the byte offset of the first line that equals `target`
// exactly (no leading/trailing whitespace, terminated by \n or end of input).
// Returns -1 if not found. Built for splitFrontmatter's "find the closing
// ---" use case — not a general-purpose line search.
func indexLine(haystack []byte, target string) int {
	t := []byte(target)
	off := 0
	for off < len(haystack) {
		end := bytes.IndexByte(haystack[off:], '\n')
		var line []byte
		if end < 0 {
			line = haystack[off:]
		} else {
			line = haystack[off : off+end]
		}
		// Strip trailing CR for CRLF files.
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if bytes.Equal(line, t) {
			return off
		}
		if end < 0 {
			return -1
		}
		off += end + 1
	}
	return -1
}
