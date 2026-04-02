package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/codebot/internal/skill"
)

func newSkillCatalog(skills []skill.Spec) *skill.Catalog {
	return skill.NewStaticCatalog(skills)
}

func TestSkillToolExecute(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	skillFile := filepath.Join(dir, "greet.md")
	os.WriteFile(skillFile, []byte("---\nname: greet\ndescription: Say hello\n---\nHello $ARGUMENTS!"), 0o644)

	tool := NewSkillTool(newSkillCatalog([]skill.Spec{
		{Name: "greet", Description: "Say hello", FilePath: skillFile, BaseDir: dir},
	}), "test-session")

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

func TestSkillToolVarExpansion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	skillFile := filepath.Join(dir, "logger.md")
	os.WriteFile(skillFile, []byte("log to ${CODEBOT_SESSION_ID}.log\nscript at ${CODEBOT_SKILL_DIR}/run.sh"), 0o644)

	tool := NewSkillTool(newSkillCatalog([]skill.Spec{
		{Name: "logger", FilePath: skillFile, BaseDir: dir},
	}), "sess-abc")

	args, _ := json.Marshal(skillArgs{Skill: "logger"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	var text string
	json.Unmarshal(result, &text)
	if !contains(text, "sess-abc.log") {
		t.Errorf("expected SESSION_ID expanded, got: %s", text)
	}
	if !contains(text, dir+"/run.sh") {
		t.Errorf("expected SKILL_DIR expanded to %s, got: %s", dir, text)
	}
}

func TestSkillToolVarClaudeAlias(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := filepath.Join(dir, "compat.md")
	os.WriteFile(f, []byte("${CLAUDE_SESSION_ID} ${CLAUDE_SKILL_DIR}"), 0o644)

	tool := NewSkillTool(newSkillCatalog([]skill.Spec{
		{Name: "compat", FilePath: f, BaseDir: dir},
	}), "s1")

	args, _ := json.Marshal(skillArgs{Skill: "compat"})
	result, _ := tool.Execute(context.Background(), args)
	var text string
	json.Unmarshal(result, &text)
	if !contains(text, "s1") || !contains(text, dir) {
		t.Errorf("CLAUDE_* aliases should work, got: %s", text)
	}
}

func TestSkillToolShellInjection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := filepath.Join(dir, "info.md")
	os.WriteFile(f, []byte("user: !`whoami`\ncount: !`echo 42`"), 0o644)

	tool := NewSkillTool(newSkillCatalog([]skill.Spec{
		{Name: "info", FilePath: f, BaseDir: dir},
	}), "")

	args, _ := json.Marshal(skillArgs{Skill: "info"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	var text string
	json.Unmarshal(result, &text)
	if !contains(text, "count: 42") {
		t.Errorf("expected shell injection expanded, got: %s", text)
	}
	// whoami should produce some non-empty output
	if contains(text, "!`whoami`") {
		t.Errorf("expected !`whoami` to be replaced, got: %s", text)
	}
}

func TestSkillToolShellInjectionError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := filepath.Join(dir, "fail.md")
	os.WriteFile(f, []byte("result: !`false`"), 0o644)

	tool := NewSkillTool(newSkillCatalog([]skill.Spec{
		{Name: "fail", FilePath: f, BaseDir: dir},
	}), "")

	args, _ := json.Marshal(skillArgs{Skill: "fail"})
	result, _ := tool.Execute(context.Background(), args)
	var text string
	json.Unmarshal(result, &text)
	if !contains(text, "[error:") {
		t.Errorf("expected error marker for failed command, got: %s", text)
	}
}

func TestSkillToolNotFound(t *testing.T) {
	t.Parallel()

	tool := NewSkillTool(nil, "")
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

	tool := NewSkillTool(newSkillCatalog([]skill.Spec{
		{Name: "deploy", FilePath: f, BaseDir: dir, DisableModelInvocation: true},
	}), "")

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

func TestSkillToolContextFork(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := filepath.Join(dir, "research.md")
	os.WriteFile(f, []byte("---\ncontext: fork\nagent: explore\n---\nResearch $ARGUMENTS"), 0o644)

	var capturedArgs json.RawMessage
	applied := false
	fakeExecutor := func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
		if !applied {
			t.Fatalf("expected invocation applier to run before fork executor")
		}
		capturedArgs = args
		return json.Marshal(map[string]string{"output": "research results"})
	}

	tool := NewSkillTool(newSkillCatalog([]skill.Spec{
		{Name: "research", FilePath: f, BaseDir: dir, Context: "fork", Agent: "explore"},
	}), "")
	tool.SetForkExecutor(fakeExecutor)
	tool.SetInvocationApplier(func(_ *skill.InvocationResult) error {
		applied = true
		return nil
	})

	args, _ := json.Marshal(skillArgs{Skill: "research", Args: "auth module"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Verify fork executor was called with correct agent and expanded task.
	var params map[string]string
	json.Unmarshal(capturedArgs, &params)
	if params["agent"] != "explore" {
		t.Errorf("expected agent=explore, got %q", params["agent"])
	}
	if !contains(params["task"], "Research auth module") {
		t.Errorf("expected expanded task, got %q", params["task"])
	}

	// Verify result is passed through from executor.
	var output map[string]string
	json.Unmarshal(result, &output)
	if output["output"] != "research results" {
		t.Errorf("expected executor result passthrough, got %s", string(result))
	}
}

func TestSkillToolContextForkDefaultAgent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := filepath.Join(dir, "task.md")
	os.WriteFile(f, []byte("---\ncontext: fork\n---\nDo stuff"), 0o644)

	var capturedArgs json.RawMessage
	tool := NewSkillTool(newSkillCatalog([]skill.Spec{
		{Name: "task", FilePath: f, BaseDir: dir, Context: "fork"},
	}), "")
	tool.SetForkExecutor(func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
		capturedArgs = args
		return json.Marshal("ok")
	})

	args, _ := json.Marshal(skillArgs{Skill: "task"})
	tool.Execute(context.Background(), args)

	var params map[string]string
	json.Unmarshal(capturedArgs, &params)
	if params["agent"] != "coder" {
		t.Errorf("expected default agent=coder, got %q", params["agent"])
	}
}

func TestSkillToolInvocationApplier(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f := filepath.Join(dir, "review.md")
	os.WriteFile(f, []byte("review $ARGUMENTS"), 0o644)

	tool := NewSkillTool(newSkillCatalog([]skill.Spec{
		{Name: "review", FilePath: f, BaseDir: dir},
	}), "")

	called := false
	tool.SetInvocationApplier(func(result *skill.InvocationResult) error {
		called = true
		if !contains(result.PromptText, "review diff") {
			t.Fatalf("unexpected prompt text: %q", result.PromptText)
		}
		return nil
	})

	args, _ := json.Marshal(skillArgs{Skill: "review", Args: "diff"})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !called {
		t.Fatal("expected invocation applier to be called")
	}
}

func TestExecuteSkillInvocationInline(t *testing.T) {
	t.Parallel()

	applied := false
	execResult, err := ExecuteSkillInvocation(context.Background(), &skill.InvocationResult{
		Spec:       skill.Spec{Name: "review"},
		PromptText: "review diff",
		Mode:       skill.ModeInline,
	}, func(result *skill.InvocationResult) error {
		applied = true
		if result.PromptText != "review diff" {
			t.Fatalf("unexpected prompt text: %q", result.PromptText)
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteSkillInvocation error: %v", err)
	}
	if !applied {
		t.Fatal("expected invocation applier to run")
	}
	if execResult.Forked {
		t.Fatalf("expected inline execution result, got %#v", execResult)
	}
	if execResult.PromptText != "review diff" {
		t.Fatalf("unexpected prompt text: %#v", execResult)
	}
}

func TestBuildSkillForkArgsIncludesModelOverride(t *testing.T) {
	t.Parallel()

	args, err := BuildSkillForkArgs(&skill.InvocationResult{
		Spec:       skill.Spec{Name: "deep-review"},
		PromptText: "fork task",
		Agent:      "plan",
		Mode:       skill.ModeFork,
		Delta: skill.Delta{
			ModelOverride: "openai/gpt-5",
		},
	})
	if err != nil {
		t.Fatalf("BuildSkillForkArgs error: %v", err)
	}

	var params map[string]string
	if err := json.Unmarshal(args, &params); err != nil {
		t.Fatalf("unmarshal fork args: %v", err)
	}
	if params["agent"] != "plan" || params["task"] != "fork task" || params["model"] != "openai/gpt-5" {
		t.Fatalf("unexpected fork args: %#v", params)
	}
}

func TestNormalizeAgentType(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"Explore", "explore"},
		{"explore", "explore"},
		{"Plan", "plan"},
		{"general-purpose", "coder"},
		{"coder", "coder"},
		{"", "coder"},
		{"custom-agent", "custom-agent"},
	}
	for _, tc := range tests {
		if got := skill.NormalizeAgentType(tc.in); got != tc.want {
			t.Errorf("NormalizeAgentType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSkillToolSetCatalog(t *testing.T) {
	t.Parallel()

	tool := NewSkillTool(nil, "")

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
	tool.SetCatalog(newSkillCatalog([]skill.Spec{{Name: "x", FilePath: f, BaseDir: dir}}))

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
			got := skill.ExpandArgs(tc.body, tc.args)
			if got != tc.want {
				t.Errorf("ExpandArgs(%q, %q)\n  got:  %q\n  want: %q", tc.body, tc.args, got, tc.want)
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
