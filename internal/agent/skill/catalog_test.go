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

// Of two skills with one name, the first wins: the caller orders them.
func TestCatalogKeepsTheFirstOfAName(t *testing.T) {
	t.Parallel()

	spec, _ := NewCatalog([]Spec{{Name: "review", Source: "project"}, {Name: "review", Source: "user"}}).Get("review")
	if spec.Source != "project" {
		t.Errorf("got the %s skill", spec.Source)
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

	without := c.Active(withoutMarker)
	if got := without.List(); len(got) != 1 || got[0].Name != "always-on" {
		t.Fatalf("frontend must be inactive without a match, got %+v", got)
	}
	if _, ok := without.Get("frontend"); ok {
		t.Fatal("Get must respect the same activation check as List")
	}
	with := c.Active(withMarker)
	if got := with.List(); len(got) != 2 {
		t.Fatalf("frontend must be active in the workspace holding a match, got %+v", got)
	}
	if _, ok := with.Get("Frontend"); !ok {
		t.Fatal("Get must find the skill where it is active, ignoring case")
	}
}

// What git keeps is not the workspace.
func TestCatalogActivationIgnoresGit(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	writeSkillFile(t, filepath.Join(cwd, ".git", "hooks", "pre-commit.py"), "")
	c := NewCatalog([]Spec{{Name: "python", Paths: []string{"**/*.py"}}})
	if got := c.Active(cwd).List(); len(got) != 0 {
		t.Fatalf("a file under .git activated %+v", got)
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

// A frozen skill runs as it was when frozen, whatever its file says since.
func TestFreeze(t *testing.T) {
	spec := fileSkill(t, "---\ndescription: d\n---\nbefore\n", false)
	frozen, err := spec.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.FilePath, []byte("---\ndescription: d\n---\nafter !`touch x`\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := frozen.prompt(t.Context(), "", ""); !strings.Contains(got, "before") {
		t.Errorf("the frozen skill reads %q", got)
	}
	if got := frozen.Privileges(); len(got) > 0 {
		t.Errorf("the frozen skill has the file's new privileges %q", got)
	}
	if got, _ := spec.prompt(t.Context(), "", ""); !strings.Contains(got, "after") {
		t.Errorf("the skill reads %q", got)
	}
}
