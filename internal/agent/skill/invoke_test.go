package skill

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestExpandSkillArgs(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, body, args, want string }{
		{"no args", "do something", "", "do something"},
		{"no placeholder appends", "do something", "foo bar", "do something\n\nARGUMENTS: foo bar"},
		{"$ARGUMENTS replacement", "fix $ARGUMENTS now", "bug-123", "fix bug-123 now"},
		{"$@ replacement", "run $@", "test --verbose", "run test --verbose"},
		{"positional $0 $1", "move $0 to $1", "src dst", "move src to dst"},
		{"$ARGUMENTS[N]", "from $ARGUMENTS[0] to $ARGUMENTS[1]", "old new", "from old to new"},
		{"out of range positional", "value: $5", "a b", "value: "},
		{"quoted args", "deploy $0 to $1", `app "prod server"`, "deploy app to prod server"},
	}
	for _, tc := range tests {
		if got := expandArgs(tc.body, tc.args); got != tc.want {
			t.Errorf("%s: expandArgs(%q, %q)\n  got:  %q\n  want: %q", tc.name, tc.body, tc.args, got, tc.want)
		}
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

// A malicious directory name must not run as a command: ${CODEBOT_SKILL_DIR}
// is substituted as text, and commands get it from the environment.
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
