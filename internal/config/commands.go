package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/codebot/internal/policy"
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
	Risk        policy.CommandRisk
	NeedsIdle   bool
	Hidden      bool
}

// LoadFileCommands discovers and loads Markdown-backed slash commands from
// user and project command directories. Project commands override user
// commands with the same name.
func LoadFileCommands(cwd string) []FileCommand {
	byName := make(map[string]FileCommand)

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
		if err != nil || !validSkillName(cmd.Name) {
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
	risk := policy.RiskLow
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
			risk = parseFrontmatterRisk(frontmatter)
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
		Risk:        risk,
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

func parseFrontmatterRisk(fm string) policy.CommandRisk {
	switch strings.ToLower(parseFrontmatterField(fm, "risk")) {
	case string(policy.RiskMedium):
		return policy.RiskMedium
	case string(policy.RiskHigh):
		return policy.RiskHigh
	default:
		return policy.RiskLow
	}
}

func normalizeAliases(aliases []string) []string {
	var normalized []string
	seen := make(map[string]bool)
	for _, alias := range aliases {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if !validSkillName(alias) || seen[alias] {
			continue
		}
		seen[alias] = true
		normalized = append(normalized, alias)
	}
	return normalized
}
