package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/skill"
)

const (
	ScopeProject = "project"
	ScopeUser    = "user"
)

var pluginIDNormalizer = regexp.MustCompile(`[^a-z0-9]+`)

type ScaffoldInput struct {
	Cwd   string
	ID    string
	Scope string
}

type ScaffoldResult struct {
	ID           string
	Scope        string
	RootDir      string
	ManifestPath string
}

func NormalizeID(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = pluginIDNormalizer.ReplaceAllString(normalized, "-")
	return strings.Trim(normalized, "-")
}

func ValidateID(id string) error {
	if !skill.ValidName(id) {
		return fmt.Errorf("plugin id %q is invalid; use lowercase letters, digits, '-' or '_'", id)
	}
	return nil
}

func Scaffold(input ScaffoldInput) (*ScaffoldResult, error) {
	id := NormalizeID(input.ID)
	if id == "" {
		return nil, fmt.Errorf("plugin id is required")
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}

	scope := strings.ToLower(strings.TrimSpace(input.Scope))
	if scope == "" {
		scope = ScopeProject
	}

	rootDir, err := scaffoldRootDir(input.Cwd, scope, id)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(rootDir); err == nil {
		return nil, fmt.Errorf("plugin %q already exists at %s", id, rootDir)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat plugin dir %s: %w", rootDir, err)
	}

	if err := os.MkdirAll(filepath.Join(rootDir, "skills"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir skills dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(rootDir, "commands"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir commands dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "skills", ".keep"), nil, 0o644); err != nil {
		return nil, fmt.Errorf("write skills keep file: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "commands", ".keep"), nil, 0o644); err != nil {
		return nil, fmt.Errorf("write commands keep file: %w", err)
	}

	manifest := Manifest{
		ID:          id,
		Name:        displayNameFromID(id),
		Version:     "0.1.0",
		Description: "TODO: describe what this plugin adds to codebot",
		SkillsDir:   "./skills",
		CommandsDir: "./commands",
	}
	manifestPath := filepath.Join(rootDir, "plugin.json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal plugin manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("write plugin manifest: %w", err)
	}

	readme := buildScaffoldREADME(id, scope)
	if err := os.WriteFile(filepath.Join(rootDir, "README.md"), []byte(readme), 0o644); err != nil {
		return nil, fmt.Errorf("write plugin readme: %w", err)
	}

	return &ScaffoldResult{
		ID:           id,
		Scope:        scope,
		RootDir:      rootDir,
		ManifestPath: manifestPath,
	}, nil
}

func scaffoldRootDir(cwd, scope, id string) (string, error) {
	switch scope {
	case ScopeProject:
		return filepath.Join(cwd, config.ConfigDir, "plugins", id), nil
	case ScopeUser:
		userDir := config.UserConfigDir()
		if userDir == "" {
			return "", fmt.Errorf("cannot resolve user config dir")
		}
		return filepath.Join(userDir, "plugins", id), nil
	default:
		return "", fmt.Errorf("unknown plugin scope %q", scope)
	}
}

func displayNameFromID(id string) string {
	parts := strings.FieldsFunc(id, func(r rune) bool {
		return r == '-' || r == '_'
	})
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func buildScaffoldREADME(id, scope string) string {
	return fmt.Sprintf(
		"# %s\n\n"+
			"A codebot plugin scaffold.\n\n"+
			"## Layout\n\n"+
			"- `plugin.json` — plugin manifest\n"+
			"- `skills/` — skill files; the filename becomes the skill name\n"+
			"- `commands/` — Markdown files for slash commands\n\n"+
			"## Scope\n\n"+
			"- %s\n\n"+
			"## Next steps\n\n"+
			"1. Edit `plugin.json` to fill in the description and version.\n"+
			"2. Add a skill file under `skills/`, e.g. `review.md`.\n"+
			"3. Add a command file under `commands/`, e.g. `triage.md`.\n"+
			"4. Run `/reload` or `/plugins list` in your project to verify it loads.\n\n"+
			"## Minimal skill example\n\n"+
			"```md\n"+
			"---\n"+
			"description: Code review assistant\n"+
			"when_to_use: When the user asks you to review a diff, hunt regressions, or flag risks\n"+
			"---\n\n"+
			"Read the change first, then return a list of issues sorted by severity.\n"+
			"```\n\n"+
			"## Minimal command example\n\n"+
			"```md\n"+
			"---\n"+
			"description: Enter the release-check flow\n"+
			"usage: /release-check [version]\n"+
			"---\n\n"+
			"Check the current workspace's changes, test status, and outstanding release risks.\n"+
			"```\n",
		id,
		scope,
	)
}
