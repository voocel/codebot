package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Skill represents a loadable skill file.
type Skill struct {
	Name                   string // validated skill name (stored lowercase)
	Description            string // from frontmatter or first line
	FilePath               string // absolute path to the .md file
	BaseDir                string // parent directory (for relative path resolution)
	Source                 string // "user" or "project"
	DisableModelInvocation bool   // when true, excluded from system prompt
}

// LoadSkills discovers and loads skills from user and project directories.
// Project skills override user skills with the same name.
func LoadSkills(cwd string) []Skill {
	byName := make(map[string]Skill)

	// 1. User-level: ~/.codebot/skills/
	if userDir := UserConfigDir(); userDir != "" {
		for _, s := range loadSkillsFromDir(filepath.Join(userDir, "skills"), "user") {
			byName[s.Name] = s
		}
	}

	// 2. Project-level: <cwd>/.codebot/skills/ (overrides user)
	for _, s := range loadSkillsFromDir(SkillsDir(cwd), "project") {
		byName[s.Name] = s
	}

	skills := make([]Skill, 0, len(byName))
	for _, s := range byName {
		skills = append(skills, s)
	}
	return skills
}

// loadSkillsFromDir scans a directory for skills.
// Supports two layouts:
//   - Root file:    skills/name.md
//   - Subdirectory: skills/name/SKILL.md (also searched recursively in nested dirs)
func loadSkillsFromDir(dir, source string) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var skills []Skill
	for _, e := range entries {
		if e.IsDir() {
			// Search for SKILL.md in the subdirectory tree.
			if s, ok := findSkillInDir(filepath.Join(dir, e.Name()), e.Name(), source); ok {
				skills = append(skills, s)
			}
			continue
		}
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		s, err := loadSkill(filepath.Join(dir, e.Name()), source)
		if err != nil {
			continue
		}
		if !validSkillName(s.Name) {
			continue
		}
		skills = append(skills, s)
	}
	return skills
}

// findSkillInDir searches for SKILL.md in dir, then recursively in subdirectories.
// dirName is used as the default skill name if frontmatter doesn't provide one.
func findSkillInDir(dir, dirName, source string) (Skill, bool) {
	// Check for SKILL.md directly in this directory.
	skillFile := filepath.Join(dir, "SKILL.md")
	if s, err := loadSkill(skillFile, source); err == nil {
		if s.Name == "skill" {
			s.Name = strings.ToLower(dirName)
		}
		if validSkillName(s.Name) {
			return s, true
		}
	}

	// Recurse into subdirectories.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Skill{}, false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if s, ok := findSkillInDir(filepath.Join(dir, e.Name()), dirName, source); ok {
			return s, true
		}
	}
	return Skill{}, false
}

func loadSkill(path, source string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	name := strings.TrimSuffix(filepath.Base(path), ".md")
	content := string(data)
	var description string
	var disableModel bool

	// Parse frontmatter.
	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		rest := content[4:]
		if fm, _, ok := strings.Cut(rest, "\n---"); ok {
			description = parseFrontmatterField(fm, "description")
			disableModel = parseFrontmatterField(fm, "disable-model-invocation") == "true"
			if fmName := parseFrontmatterField(fm, "name"); fmName != "" {
				name = fmName
			}
		}
	}

	// Normalize to lowercase for consistent lookup and override.
	name = strings.ToLower(name)

	if description == "" {
		description = firstLine(StripFrontmatter(content), 60)
	}

	return Skill{
		Name:                   name,
		Description:            description,
		FilePath:               path,
		BaseDir:                filepath.Dir(path),
		Source:                 source,
		DisableModelInvocation: disableModel,
	}, nil
}

// validSkillName checks that a skill name conforms to [a-zA-Z0-9_-]+, max 64 chars,
// no leading/trailing hyphens or underscores. Name is expected to be lowercased already.
var reSkillName = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`)

func validSkillName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	return reSkillName.MatchString(name)
}

// parseFrontmatterField extracts a field value from YAML-like frontmatter text.
func parseFrontmatterField(fm, field string) string {
	prefix := field + ":"
	for line := range strings.SplitSeq(fm, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			val := strings.TrimSpace(line[len(prefix):])
			val = strings.Trim(val, `"'`)
			return val
		}
	}
	return ""
}

// StripFrontmatter removes a leading YAML frontmatter block (--- delimited) from content.
func StripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return content
	}
	rest := content[4:]
	_, after, ok := strings.Cut(rest, "\n---")
	if !ok {
		return content
	}
	return strings.TrimLeft(after, "\r\n")
}

// firstLine returns the first non-empty line, truncated to maxLen characters.
func firstLine(s string, maxLen int) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > maxLen {
			return string(runes[:maxLen])
		}
		return line
	}
	return ""
}

// FormatSkillsForPrompt generates the XML block injected into the system prompt.
// Skills with DisableModelInvocation=true are excluded.
func FormatSkillsForPrompt(skills []Skill) string {
	var promptable []Skill
	for _, s := range skills {
		if !s.DisableModelInvocation {
			promptable = append(promptable, s)
		}
	}
	if len(promptable) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("The following skills provide specialized instructions for specific tasks.\n")
	sb.WriteString("Use the read tool to load a skill's file when the task matches its description.\n")
	sb.WriteString("When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the .md file) and use that absolute path in tool commands.\n\n")
	sb.WriteString("<available_skills>")
	for _, s := range promptable {
		fmt.Fprintf(&sb, "\n  <skill>\n    <name>%s</name>\n    <description>%s</description>\n    <location>%s</location>\n  </skill>",
			escapeXML(s.Name), escapeXML(s.Description), escapeXML(s.FilePath))
	}
	sb.WriteString("\n</available_skills>")
	return sb.String()
}

func escapeXML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
