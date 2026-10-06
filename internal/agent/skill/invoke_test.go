package skill

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// fileSkill writes a skill file and loads it, privileged or not.
func fileSkill(t *testing.T, content string, privileged bool) Spec {
	t.Helper()
	dir := t.TempDir()
	writeSkillFile(t, dir+"/s.md", content)
	specs, errs := LoadDir(dir)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	specs[0].Privileged = privileged
	return specs[0]
}

func invoke(t *testing.T, spec Spec, in InvokeInput) *Invocation {
	t.Helper()
	in.Name = spec.Name
	inv, err := NewCatalog([]Spec{spec}).Invoke(context.Background(), in)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	return inv
}

func TestInvokeExpandsVariablesAndShell(t *testing.T) {
	t.Parallel()

	spec := fileSkill(t, "log to ${CODEBOT_SESSION_ID}.log, ${CLAUDE_SKILL_DIR}/run.sh\ncount: !`echo 42`\nfail: !`false`", true)
	inv := invoke(t, spec, InvokeInput{SessionID: "sess-abc", By: ByUser})
	for _, want := range []string{"sess-abc.log", spec.BaseDir + "/run.sh", "count: 42", "fail: [error:", `<skill name="s">`} {
		if !strings.Contains(inv.Prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, inv.Prompt)
		}
	}
}

// A skill without privileges keeps its text but neither runs its commands
// nor allows tools nor picks a model.
func TestUntrustedSkillLosesPrivileges(t *testing.T) {
	t.Parallel()

	spec := fileSkill(t, "---\nmodel: gpt-5\nallowed-tools: bash\n---\nresult: !`echo 42`", true)
	if inv := invoke(t, spec, InvokeInput{By: ByUser}); !strings.Contains(inv.Prompt, "result: 42") || inv.Model != "gpt-5" || len(inv.AllowedTools) != 1 {
		t.Fatalf("a trusted skill keeps its privileges, got %+v", inv)
	}
	spec.Privileged = false
	inv := invoke(t, spec, InvokeInput{By: ByUser})
	if len(inv.AllowedTools) != 0 || inv.Model != "" {
		t.Fatalf("expected allowed tools and model stripped, got %+v", inv)
	}
	if !strings.Contains(inv.Prompt, "!`echo 42`") {
		t.Fatalf("expected the shell command to stay literal, got %q", inv.Prompt)
	}
}

func TestInvokeChecksWhoInvokes(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog([]Spec{
		{Name: "manual", DisableModelInvocation: true},
		{Name: "background", DisableUserInvocation: true},
	})
	if _, err := catalog.Invoke(context.Background(), InvokeInput{Name: "manual", By: ByModel}); err != ErrModelInvocationDenied {
		t.Errorf("model invoking a manual skill: %v", err)
	}
	if _, err := catalog.Invoke(context.Background(), InvokeInput{Name: "background", By: ByUser}); err != ErrUserInvocationDenied {
		t.Errorf("user invoking a model-only skill: %v", err)
	}
}

func TestInvokeRejectsInactivePathScopedSkill(t *testing.T) {
	t.Parallel()

	catalog := NewCatalog([]Spec{{Name: "frontend", Paths: []string{"web/**"}}}).Active(t.TempDir())
	_, err := catalog.Invoke(context.Background(), InvokeInput{Name: "frontend", By: ByUser})
	if err != ErrNotFound {
		t.Fatalf("expected inactive skill to behave as not found, got %v", err)
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
			got := expandArgs(tc.body, tc.args)
			if got != tc.want {
				t.Errorf("expandArgs(%q, %q)\n  got:  %q\n  want: %q", tc.body, tc.args, got, tc.want)
			}
		})
	}
}

func TestPrivileges(t *testing.T) {
	t.Parallel()

	spec := fileSkill(t, "---\nmodel: gpt-5\nallowed-tools: [\"Bash(git *)\"]\n---\nDiff: !`git diff`", false)
	want := []string{"runs `git diff`", "allows `Bash(git *)`", "picks gpt-5"}
	if got := spec.Privileges(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("privileges = %q, want %q", got, want)
	}
	if got := fileSkill(t, "Just text.", false).Privileges(); len(got) != 0 {
		t.Errorf("a plain skill has privileges %q", got)
	}
}

// What a skill runs is what its text holds, as Privileges lists it: the
// name of its directory, put in by a variable, runs nothing, and reaches
// the commands as an environment variable.
func TestSkillDirectoryRunsNothing(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "h!`echo PWNED`")
	writeSkillFile(t, filepath.Join(dir, "SKILL.md"), "---\nname: helper\n---\nNotes are in ${CODEBOT_SKILL_DIR}.\nfile: !`printf %s \"$CODEBOT_SKILL_DIR\"`")
	spec, err := LoadFile(filepath.Join(dir, "SKILL.md"), "helper")
	if err != nil {
		t.Fatal(err)
	}
	spec.Privileged = true
	inv := invoke(t, spec, InvokeInput{By: ByUser})
	if strings.Contains(inv.Prompt, "PWNED") && !strings.Contains(inv.Prompt, "Notes are in "+spec.BaseDir+".") {
		t.Fatalf("the directory's name ran:\n%s", inv.Prompt)
	}
	if !strings.Contains(inv.Prompt, "file: "+spec.BaseDir) {
		t.Errorf("the command did not get the directory:\n%s", inv.Prompt)
	}
	if got := spec.Privileges(); len(got) != 1 {
		t.Errorf("privileges %q", got)
	}
}
