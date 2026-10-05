package skill

import (
	"context"
	"strings"
	"testing"
)

// fileSkill writes a skill file and loads it as from source.
func fileSkill(t *testing.T, content, source string) Spec {
	t.Helper()
	dir := t.TempDir()
	writeSkillFile(t, dir+"/s.md", content)
	specs, errs := LoadDir(dir)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	specs[0].Source = source
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

	spec := fileSkill(t, "log to ${CODEBOT_SESSION_ID}.log, ${CLAUDE_SKILL_DIR}/run.sh\ncount: !`echo 42`\nfail: !`false`", "project")
	inv := invoke(t, spec, InvokeInput{SessionID: "sess-abc", By: ByUser})
	for _, want := range []string{"sess-abc.log", spec.BaseDir + "/run.sh", "count: 42", "fail: [error:", `<skill name="s">`} {
		if !strings.Contains(inv.Prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, inv.Prompt)
		}
	}
}

// What an untrusted plugin's skill may do is decided by its source when it
// is invoked, also for a skill loaded from a trusted one first.
func TestUntrustedSkillLosesPrivileges(t *testing.T) {
	t.Parallel()

	spec := fileSkill(t, "---\nmodel: gpt-5\nallowed-tools: bash\n---\nresult: !`echo 42`", "bundled")
	if inv := invoke(t, spec, InvokeInput{By: ByUser}); !strings.Contains(inv.Prompt, "result: 42") || inv.Model != "gpt-5" || len(inv.AllowedTools) != 1 {
		t.Fatalf("a trusted skill keeps its privileges, got %+v", inv)
	}
	spec.Source = "remote"
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

	catalog := NewCatalog([]Spec{{Name: "frontend", Paths: []string{"web/**"}}})
	_, err := catalog.Invoke(context.Background(), InvokeInput{Name: "frontend", Cwd: t.TempDir(), By: ByUser})
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
