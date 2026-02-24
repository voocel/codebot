package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidSkillName(t *testing.T) {
	t.Parallel()

	valid := []string{"a", "commit", "code-review", "my-skill-1", "a1b"}
	for _, name := range valid {
		if !validSkillName(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}

	invalid := []string{
		"", "-start", "end-", "has--double", "UPPER", "has space",
		"has.dot", "has_underscore",
		// 65 chars
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaа",
	}
	for _, name := range invalid {
		if validSkillName(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestParseFrontmatterField(t *testing.T) {
	t.Parallel()

	fm := "name: my-skill\ndescription: \"A test skill\"\ndisable-model-invocation: true"
	if got := parseFrontmatterField(fm, "name"); got != "my-skill" {
		t.Errorf("name = %q, want %q", got, "my-skill")
	}
	if got := parseFrontmatterField(fm, "description"); got != "A test skill" {
		t.Errorf("description = %q, want %q", got, "A test skill")
	}
	if got := parseFrontmatterField(fm, "disable-model-invocation"); got != "true" {
		t.Errorf("disable-model-invocation = %q, want %q", got, "true")
	}
	if got := parseFrontmatterField(fm, "nonexistent"); got != "" {
		t.Errorf("nonexistent = %q, want empty", got)
	}
}

func TestStripFrontmatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
	}{
		{"no frontmatter", "hello world", "hello world"},
		{"with frontmatter", "---\nname: test\n---\ncontent here", "content here"},
		{"unclosed frontmatter", "---\nname: test\nno closing", "---\nname: test\nno closing"},
	}
	for _, tc := range tests {
		if got := StripFrontmatter(tc.input); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFormatSkillsForPrompt(t *testing.T) {
	t.Parallel()

	skills := []Skill{
		{Name: "commit", Description: "Git commit helper", FilePath: "/skills/commit.md"},
		{Name: "hidden", Description: "Hidden skill", FilePath: "/skills/hidden.md", DisableModelInvocation: true},
		{Name: "review", Description: "Code reviewer", FilePath: "/skills/review.md"},
	}

	result := FormatSkillsForPrompt(skills)

	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if !contains(result, "<name>commit</name>") {
		t.Error("missing commit skill")
	}
	if !contains(result, "<name>review</name>") {
		t.Error("missing review skill")
	}
	if contains(result, "<name>hidden</name>") {
		t.Error("disabled skill should be excluded")
	}
	if !contains(result, "<available_skills>") {
		t.Error("missing XML wrapper")
	}
	// Verify the relative path guidance line is present.
	if !contains(result, "resolve it against the skill directory") {
		t.Error("missing relative path resolution guidance")
	}
}

func TestFormatSkillsXMLEscape(t *testing.T) {
	t.Parallel()

	skills := []Skill{
		{Name: "test", Description: "Uses <tags> & stuff", FilePath: "/path/to/test.md"},
	}
	result := FormatSkillsForPrompt(skills)
	if !strings.Contains(result, "&lt;tags&gt;") {
		t.Error("description should be XML-escaped")
	}
	if !strings.Contains(result, "&amp; stuff") {
		t.Error("ampersand should be XML-escaped")
	}
}

func TestFormatSkillsEmpty(t *testing.T) {
	t.Parallel()

	if result := FormatSkillsForPrompt(nil); result != "" {
		t.Errorf("expected empty, got %q", result)
	}
	if result := FormatSkillsForPrompt([]Skill{{Name: "x", DisableModelInvocation: true}}); result != "" {
		t.Errorf("expected empty when all disabled, got %q", result)
	}
}

func TestLoadSkillsFromDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Root file: dir/commit.md
	writeFile(t, filepath.Join(dir, "commit.md"), "---\ndescription: Commit helper\n---\nDo the commit")

	// Subdirectory: dir/review/SKILL.md
	reviewDir := filepath.Join(dir, "review")
	os.MkdirAll(reviewDir, 0o755)
	writeFile(t, filepath.Join(reviewDir, "SKILL.md"), "---\ndescription: Code review\n---\nReview code")

	// Nested subdirectory: dir/deploy/src/SKILL.md
	nestedDir := filepath.Join(dir, "deploy", "src")
	os.MkdirAll(nestedDir, 0o755)
	writeFile(t, filepath.Join(nestedDir, "SKILL.md"), "---\ndescription: Deploy helper\n---\nDeploy stuff")

	skills := loadSkillsFromDir(dir, "test")
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(skills))
	}

	byName := make(map[string]Skill, len(skills))
	for _, s := range skills {
		byName[s.Name] = s
	}
	if s, ok := byName["commit"]; !ok {
		t.Error("missing commit skill")
	} else if s.Description != "Commit helper" {
		t.Errorf("commit description = %q", s.Description)
	}
	if s, ok := byName["review"]; !ok {
		t.Error("missing review skill")
	} else if s.Description != "Code review" {
		t.Errorf("review description = %q", s.Description)
	}
	if s, ok := byName["deploy"]; !ok {
		t.Error("missing deploy skill from nested dir")
	} else if s.Description != "Deploy helper" {
		t.Errorf("deploy description = %q", s.Description)
	}
}

func TestBuildSystemPromptWithSkills(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{Name: "read", Description: "Read files"},
		{Name: "bash", Description: "Run commands"},
	}
	skills := []Skill{
		{Name: "commit", Description: "Git commit", FilePath: "/skills/commit.md"},
	}

	prompt := BuildSystemPrompt("/tmp/ws", ContextFiles{}, tools, skills)

	if !contains(prompt, "## Skills") {
		t.Error("prompt should contain skills section")
	}
	if !contains(prompt, "<name>commit</name>") {
		t.Error("prompt should contain commit skill")
	}
}

func TestBuildSystemPromptSkillsNoReadTool(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{Name: "bash", Description: "Run commands"},
	}
	skills := []Skill{
		{Name: "commit", Description: "Git commit", FilePath: "/skills/commit.md"},
	}

	prompt := BuildSystemPrompt("/tmp/ws", ContextFiles{}, tools, skills)

	if contains(prompt, "## Skills") {
		t.Error("prompt should not contain skills when read tool is absent")
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
