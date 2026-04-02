package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	t.Parallel()

	valid := []string{
		"a", "commit", "code-review", "my-skill-1", "a1b",
		"has_underscore", "code_review", "has--double", "my_skill_1",
	}
	for _, name := range valid {
		if !ValidName(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}

	invalid := []string{
		"", "-start", "end-", "_start", "end_", "has space",
		"has.dot",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaа",
	}
	for _, name := range invalid {
		if ValidName(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestStripFrontmatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
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

func TestRenderListing(t *testing.T) {
	t.Parallel()

	skills := []Spec{
		{Name: "commit", Description: "Git commit helper", FilePath: "/skills/commit.md"},
		{Name: "hidden", Description: "Hidden skill", FilePath: "/skills/hidden.md", DisableModelInvocation: true},
		{Name: "review", Description: "Code reviewer", FilePath: "/skills/review.md"},
		{Name: "conventions", Description: "API conventions", FilePath: "/skills/conventions.md", DisableUserInvocation: true},
	}

	result := RenderListing(skills, DefaultListingOptions())

	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(result, "- commit: Git commit helper") {
		t.Error("missing commit skill entry")
	}
	if !strings.Contains(result, "- review: Code reviewer") {
		t.Error("missing review skill entry")
	}
	if !strings.Contains(result, "- conventions: API conventions") {
		t.Error("user-invocable=false skill should still appear in prompt for LLM")
	}
	if strings.Contains(result, "hidden") {
		t.Error("disabled skill should be excluded")
	}
	if !strings.Contains(result, "Skill tool") {
		t.Error("should reference the Skill tool")
	}
}

func TestRenderListingSpecialChars(t *testing.T) {
	t.Parallel()

	result := RenderListing([]Spec{
		{Name: "test", Description: "Uses <tags> & stuff", FilePath: "/path/to/test.md"},
	}, DefaultListingOptions())
	if !strings.Contains(result, "Uses <tags> & stuff") {
		t.Error("description should appear in output")
	}
}

func TestRenderListingEmpty(t *testing.T) {
	t.Parallel()

	if result := RenderListing(nil, DefaultListingOptions()); result != "" {
		t.Errorf("expected empty, got %q", result)
	}
	if result := RenderListing([]Spec{{Name: "x", DisableModelInvocation: true}}, DefaultListingOptions()); result != "" {
		t.Errorf("expected empty when all disabled, got %q", result)
	}
}

func TestLoadFromDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	writeSkillFile(t, filepath.Join(dir, "commit.md"), "---\ndescription: Commit helper\n---\nDo the commit")

	reviewDir := filepath.Join(dir, "review")
	if err := os.MkdirAll(reviewDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillFile(t, filepath.Join(reviewDir, "SKILL.md"), "---\ndescription: Code review\n---\nReview code")

	nestedDir := filepath.Join(dir, "deploy", "src")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillFile(t, filepath.Join(nestedDir, "SKILL.md"), "---\ndescription: Deploy helper\n---\nDeploy stuff")

	writeSkillFile(t, filepath.Join(dir, "Code_Review.md"), "---\ndescription: Uppercase test\n---\nReview")

	skills := LoadFromDir(dir, "test")
	if len(skills) != 4 {
		t.Fatalf("expected 4 skills, got %d", len(skills))
	}

	byName := make(map[string]Spec, len(skills))
	for _, spec := range skills {
		byName[spec.Name] = spec
	}
	if spec, ok := byName["commit"]; !ok {
		t.Error("missing commit skill")
	} else if spec.Description != "Commit helper" {
		t.Errorf("commit description = %q", spec.Description)
	}
	if spec, ok := byName["review"]; !ok {
		t.Error("missing review skill")
	} else if spec.Description != "Code review" {
		t.Errorf("review description = %q", spec.Description)
	}
	if spec, ok := byName["deploy"]; !ok {
		t.Error("missing deploy skill from nested dir")
	} else if spec.Description != "Deploy helper" {
		t.Errorf("deploy description = %q", spec.Description)
	}
	if spec, ok := byName["code_review"]; !ok {
		t.Error("missing code_review skill (from Code_Review.md)")
	} else if spec.Description != "Uppercase test" {
		t.Errorf("code_review description = %q", spec.Description)
	}
}

func writeSkillFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
