package skill

import (
	"os"
	"path/filepath"
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
		if got := stripFrontmatter(tc.input); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLoadDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeSkillFile(t, filepath.Join(dir, "commit.md"), "---\ndescription: Commit helper\nallowed-tools: [bash, read]\n---\nDo the commit")
	writeSkillFile(t, filepath.Join(dir, "review", "SKILL.md"), "---\ndescription: Code review\ncontext: fork\n---\nReview code")
	writeSkillFile(t, filepath.Join(dir, "deploy", "src", "SKILL.md"), "Deploy stuff")
	writeSkillFile(t, filepath.Join(dir, "Code_Review.md"), "---\nuser-invocable: false\n---\nReview")

	specs, errs := LoadDir(dir)
	if len(errs) > 0 {
		t.Fatalf("LoadDir errors: %v", errs)
	}
	byName := make(map[string]Spec, len(specs))
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	if len(byName) != 4 {
		t.Fatalf("expected 4 skills, got %v", byName)
	}
	if s := byName["commit"]; s.Description != "Commit helper" || len(s.AllowedTools) != 2 || s.Context != "inline" {
		t.Errorf("commit = %+v", s)
	}
	if s := byName["review"]; s.Description != "Code review" || s.Context != "fork" || s.BaseDir != filepath.Join(dir, "review") {
		t.Errorf("review = %+v", s)
	}
	if s := byName["deploy"]; s.Description != "Deploy stuff" {
		t.Errorf("deploy from a nested dir, described by its first line = %+v", s)
	}
	if s := byName["code_review"]; !s.DisableUserInvocation {
		t.Errorf("code_review = %+v", s)
	}
}

func TestLoadDirReportsInvalidSkill(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeSkillFile(t, filepath.Join(dir, "Bad Skill.md"), "---\ndescription: bad\n---\nbody")
	writeSkillFile(t, filepath.Join(dir, "empty", "notes.txt"), "no skill here")

	specs, errs := LoadDir(dir)
	if len(specs) != 0 || len(errs) != 2 {
		t.Fatalf("expected no skills and two errors, got %d skills, errors %v", len(specs), errs)
	}
}

// Of two skills with one name, the more trusted source wins, whichever came
// first; among equals, the later one.
func TestCatalogPrefersTheMoreTrustedSource(t *testing.T) {
	t.Parallel()

	for _, order := range [][]Spec{
		{{Name: "review", Source: "bundled"}, {Name: "review", Source: "project"}},
		{{Name: "review", Source: "project"}, {Name: "review", Source: "remote"}},
		{{Name: "review", Source: "project", Description: "first"}, {Name: "review", Source: "project", Description: "later"}},
	} {
		spec, _ := NewCatalog(order).Get("review", "")
		want := order[1]
		if order[1].Source == "remote" {
			want = order[0]
		}
		if spec.Source != want.Source || spec.Description != want.Description {
			t.Errorf("from %+v got %+v", order, spec)
		}
	}
}

// A skill gated on Paths is active only in a workspace holding a match, so a
// conversation that moves into a worktree sees the skills of the worktree.
func TestCatalogActivationFollowsTheWorkspace(t *testing.T) {
	t.Parallel()

	withMarker, withoutMarker := t.TempDir(), t.TempDir()
	writeSkillFile(t, filepath.Join(withMarker, "web", "app", "index.ts"), "")

	c := NewCatalog([]Spec{
		{Name: "always-on"},
		{Name: "frontend", Paths: []string{"web/**"}},
	})

	if got := c.List(withoutMarker); len(got) != 1 || got[0].Name != "always-on" {
		t.Fatalf("frontend must be inactive without a match, got %+v", got)
	}
	if _, ok := c.Get("frontend", withoutMarker); ok {
		t.Fatal("Get must respect the same activation check as List")
	}
	if got := c.List(withMarker); len(got) != 2 {
		t.Fatalf("frontend must be active in the workspace holding a match, got %+v", got)
	}
	if _, ok := c.Get("Frontend", withMarker); !ok {
		t.Fatal("Get must find the skill where it is active, ignoring case")
	}
}

func writeSkillFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
