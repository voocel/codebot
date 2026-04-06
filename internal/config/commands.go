package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/codebot/internal/skill"
)

// FileCommand is a user-defined slash command loaded from a Markdown file.
// Its body is treated as a prompt template with optional frontmatter metadata.
type FileCommand struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string
	Content     string
	Source      string // "user" or "project"
	FilePath    string
	Category    string // prompt/info/session/config/plan/exit
	NeedsIdle   bool
	Hidden      bool
}

type ExtraCommandSource struct {
	Path   string
	Source string
}

// LoadFileCommands discovers and loads Markdown-backed slash commands from
// user, project, and extra paths (e.g. skill-declared command files).
// Project commands override user commands; extra paths have lowest priority.
func LoadFileCommands(cwd string, extraPaths ...string) []FileCommand {
	extraSources := make([]ExtraCommandSource, 0, len(extraPaths))
	for _, path := range extraPaths {
		extraSources = append(extraSources, ExtraCommandSource{Path: path, Source: "skill"})
	}
	return LoadFileCommandsWithSources(cwd, extraSources...)
}

// LoadFileCommandsWithSources discovers Markdown-backed slash commands from
// user, project, and extra plugin/skill paths. Project commands override user
// commands; extra paths have lowest priority.
func LoadFileCommandsWithSources(cwd string, extraSources ...ExtraCommandSource) []FileCommand {
	byName := make(map[string]FileCommand)

	// Extra paths first (lowest priority — overridden by user/project).
	for _, extra := range extraSources {
		path := strings.TrimSpace(extra.Path)
		if path == "" {
			continue
		}
		source := strings.TrimSpace(extra.Source)
		if source == "" {
			source = "extra"
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.IsDir() {
			for _, cmd := range loadFileCommandsFromDir(path, source) {
				byName[cmd.Name] = cmd
			}
			continue
		}
		cmd, err := loadFileCommand(path, source)
		if err != nil || !skill.ValidName(cmd.Name) {
			continue
		}
		byName[cmd.Name] = cmd
	}

	if userDir := UserConfigDir(); userDir != "" {
		for _, cmd := range loadFileCommandsFromDir(filepath.Join(userDir, "commands"), "user") {
			byName[cmd.Name] = cmd
		}
	}

	for _, cmd := range loadFileCommandsFromDir(CommandsDir(cwd), "project") {
		byName[cmd.Name] = cmd
	}

	commands := make([]FileCommand, 0, len(byName))
	for _, cmd := range byName {
		commands = append(commands, cmd)
	}
	return commands
}

func LoadCommandsFromDir(dir, source string) []FileCommand {
	return loadFileCommandsFromDir(dir, source)
}

func ValidateCommandsDir(dir, source string) ([]FileCommand, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{err}
	}

	var commands []FileCommand
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		cmd, err := loadFileCommand(path, source)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if !skill.ValidName(cmd.Name) {
			errs = append(errs, fmt.Errorf("%s: invalid command name %q", path, cmd.Name))
			continue
		}
		commands = append(commands, cmd)
	}
	return commands, errs
}

func loadFileCommandsFromDir(dir, source string) []FileCommand {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var commands []FileCommand
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		cmd, err := loadFileCommand(path, source)
		if err != nil || !skill.ValidName(cmd.Name) {
			continue
		}
		commands = append(commands, cmd)
	}
	return commands
}

func loadFileCommand(path, source string) (FileCommand, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileCommand{}, err
	}

	content := string(data)
	name := strings.TrimSuffix(filepath.Base(path), ".md")
	description := ""
	usage := ""
	var aliases []string
	category := "prompt"
	var needsIdle bool
	var hidden bool

	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		rest := content[4:]
		if idx := strings.Index(rest, "\n---"); idx >= 0 {
			frontmatter := rest[:idx]
			after := rest[idx+4:]
			if len(after) > 0 && (after[0] == '\n' || after[0] == '\r') {
				after = strings.TrimLeft(after, "\r\n")
			}
			content = after

			if fmName := parseFrontmatterField(frontmatter, "name"); fmName != "" {
				name = strings.ToLower(fmName)
			}
			description = parseFrontmatterField(frontmatter, "description")
			usage = parseFrontmatterField(frontmatter, "usage")
			aliases = append(aliases, parseFrontmatterList(frontmatter, "aliases")...)
			aliases = append(aliases, parseFrontmatterList(frontmatter, "alias")...)
			category = parseFrontmatterCategory(frontmatter)
			needsIdle = parseFrontmatterBool(frontmatter, "needs-idle") ||
				parseFrontmatterBool(frontmatter, "needs_idle")
			hidden = parseFrontmatterBool(frontmatter, "hidden")
		}
	}

	name = strings.ToLower(name)
	if description == "" {
		description = firstLine(content, 60)
	}
	if usage == "" {
		usage = "/" + name
	}

	return FileCommand{
		Name:        name,
		Aliases:     normalizeAliases(aliases),
		Description: description,
		Usage:       usage,
		Content:     strings.TrimSpace(content),
		Source:      source,
		FilePath:    path,
		Category:    category,
		NeedsIdle:   needsIdle,
		Hidden:      hidden,
	}, nil
}

func parseFrontmatterList(fm, field string) []string {
	raw := parseFrontmatterField(fm, field)
	if raw == "" {
		return nil
	}
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	parts := strings.Split(raw, ",")
	var values []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"'`)
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

func parseFrontmatterBool(fm, field string) bool {
	return strings.EqualFold(parseFrontmatterField(fm, field), "true")
}

func parseFrontmatterCategory(fm string) string {
	category := strings.ToLower(strings.TrimSpace(parseFrontmatterField(fm, "category")))
	if category == "" {
		return "prompt"
	}
	return category
}

func parseFrontmatterField(fm, field string) string {
	prefix := field + ":"
	for line := range strings.SplitSeq(fm, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			val := strings.TrimSpace(line[len(prefix):])
			return strings.Trim(val, `"'`)
		}
	}
	return ""
}

func firstLine(s string, maxLen int) string {
	return skill.FirstLine(s, maxLen)
}

func normalizeAliases(aliases []string) []string {
	var normalized []string
	seen := make(map[string]bool)
	for _, alias := range aliases {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if !skill.ValidName(alias) || seen[alias] {
			continue
		}
		seen[alias] = true
		normalized = append(normalized, alias)
	}
	return normalized
}
