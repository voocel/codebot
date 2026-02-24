package config

import (
	"os"
	"path/filepath"
	"strings"
)

// PromptTemplate is a user-defined prompt template loaded from a .md file.
type PromptTemplate struct {
	Name        string // filename without .md extension
	Description string // from frontmatter or first non-empty line (max 60 chars)
	Content     string // template body (after frontmatter)
	Source      string // "user" or "project"
	FilePath    string // absolute path to the template file
}

// LoadPromptTemplates discovers and loads .md templates from user and project directories.
// Project templates override user templates with the same name.
func LoadPromptTemplates(cwd string) []PromptTemplate {
	byName := make(map[string]PromptTemplate)

	// 1. User-level: ~/.codebot/prompts/
	if userDir := UserConfigDir(); userDir != "" {
		for _, t := range loadTemplatesFromDir(filepath.Join(userDir, "prompts"), "user") {
			byName[t.Name] = t
		}
	}

	// 2. Project-level: <cwd>/.codebot/prompts/ (overrides user)
	for _, t := range loadTemplatesFromDir(PromptsDir(cwd), "project") {
		byName[t.Name] = t
	}

	templates := make([]PromptTemplate, 0, len(byName))
	for _, t := range byName {
		templates = append(templates, t)
	}
	return templates
}

func loadTemplatesFromDir(dir, source string) []PromptTemplate {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var templates []PromptTemplate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		t, err := loadTemplate(path, source)
		if err != nil {
			continue
		}
		templates = append(templates, t)
	}
	return templates
}

func loadTemplate(path, source string) (PromptTemplate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PromptTemplate{}, err
	}

	name := strings.TrimSuffix(filepath.Base(path), ".md")
	content := string(data)
	description := ""

	// Parse optional frontmatter (--- delimited).
	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		rest := content[4:] // skip opening "---\n"
		if idx := strings.Index(rest, "\n---"); idx >= 0 {
			frontmatter := rest[:idx]
			// Skip past closing "---\n"
			after := rest[idx+4:]
			if len(after) > 0 && (after[0] == '\n' || after[0] == '\r') {
				after = strings.TrimLeft(after, "\r\n")
			}
			content = after
			description = parseFrontmatterDescription(frontmatter)
		}
	}

	// Fallback: use first non-empty line as description.
	if description == "" {
		description = firstLine(content, 60)
	}

	return PromptTemplate{
		Name:        name,
		Description: description,
		Content:     strings.TrimSpace(content),
		Source:      source,
		FilePath:    path,
	}, nil
}

// parseFrontmatterDescription extracts "description: ..." from frontmatter text.
func parseFrontmatterDescription(fm string) string {
	return parseFrontmatterField(fm, "description")
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
