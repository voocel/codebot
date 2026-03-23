package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/codebot/internal/config"
)

func TestSkillToolExecute(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	skillFile := filepath.Join(dir, "greet.md")
	os.WriteFile(skillFile, []byte("---\nname: greet\ndescription: Say hello\n---\nHello $ARGUMENTS!"), 0o644)

	tool := NewSkillTool([]config.Skill{
		{Name: "greet", Description: "Say hello", FilePath: skillFile, BaseDir: dir},
	})

	args, _ := json.Marshal(skillArgs{Skill: "greet", Args: "World"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	var text string
	if err := json.Unmarshal(result, &text); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if !contains(text, "Hello World!") {
		t.Errorf("expected $ARGUMENTS expanded, got: %s", text)
	}
	if !contains(text, `<skill name="greet">`) {
		t.Errorf("expected skill wrapper, got: %s", text)
	}
}

func TestSkillToolNotFound(t *testing.T) {
	t.Parallel()

	tool := NewSkillTool(nil)
	args, _ := json.Marshal(skillArgs{Skill: "nonexistent"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	var text string
	json.Unmarshal(result, &text)
	if !contains(text, "not found") {
		t.Errorf("expected not-found message, got: %s", text)
	}
}

func TestSkillToolDisableModelInvocation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := filepath.Join(dir, "deploy.md")
	os.WriteFile(f, []byte("deploy stuff"), 0o644)

	tool := NewSkillTool([]config.Skill{
		{Name: "deploy", FilePath: f, BaseDir: dir, DisableModelInvocation: true},
	})

	args, _ := json.Marshal(skillArgs{Skill: "deploy"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	var text string
	json.Unmarshal(result, &text)
	if !contains(text, "manual invocation only") {
		t.Errorf("expected manual-only message, got: %s", text)
	}
}

func TestSkillToolSetSkills(t *testing.T) {
	t.Parallel()

	tool := NewSkillTool(nil)

	args, _ := json.Marshal(skillArgs{Skill: "x"})
	result, _ := tool.Execute(context.Background(), args)
	var text string
	json.Unmarshal(result, &text)
	if !contains(text, "not found") {
		t.Fatal("expected not found before SetSkills")
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "x.md")
	os.WriteFile(f, []byte("skill content"), 0o644)
	tool.SetSkills([]config.Skill{{Name: "x", FilePath: f, BaseDir: dir}})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error after SetSkills: %v", err)
	}
	json.Unmarshal(result, &text)
	if !contains(text, "skill content") {
		t.Errorf("expected skill content after SetSkills, got: %s", text)
	}
}

func TestExpandSkillArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		args string
		want string
	}{
		{
			name: "no args",
			body: "do something",
			args: "",
			want: "do something",
		},
		{
			name: "no placeholder appends",
			body: "do something",
			args: "foo bar",
			want: "do something\n\nARGUMENTS: foo bar",
		},
		{
			name: "$ARGUMENTS replacement",
			body: "fix $ARGUMENTS now",
			args: "bug-123",
			want: "fix bug-123 now",
		},
		{
			name: "$@ replacement",
			body: "run $@",
			args: "test --verbose",
			want: "run test --verbose",
		},
		{
			name: "positional $0 $1",
			body: "move $0 to $1",
			args: "src dst",
			want: "move src to dst",
		},
		{
			name: "$ARGUMENTS[N]",
			body: "from $ARGUMENTS[0] to $ARGUMENTS[1]",
			args: "old new",
			want: "from old to new",
		},
		{
			name: "out of range positional",
			body: "value: $5",
			args: "a b",
			want: "value: ",
		},
		{
			name: "quoted args",
			body: "deploy $0 to $1",
			args: `app "prod server"`,
			want: "deploy app to prod server",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExpandSkillArgs(tc.body, tc.args)
			if got != tc.want {
				t.Errorf("ExpandSkillArgs(%q, %q)\n  got:  %q\n  want: %q", tc.body, tc.args, got, tc.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
