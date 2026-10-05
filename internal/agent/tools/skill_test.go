package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"

	"github.com/voocel/codebot/internal/agent/skill"
)

// skillCatalog loads the skill files given, by name, as project skills.
func skillCatalog(t *testing.T, files map[string]string) func() *skill.Catalog {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	specs, errs := skill.LoadDir(dir)
	if len(errs) > 0 {
		t.Fatalf("load skills: %v", errs)
	}
	for i := range specs {
		specs[i].Source = "project"
	}
	catalog := skill.NewCatalog(specs)
	return func() *skill.Catalog { return catalog }
}

func ignoreInvocation(*skill.Invocation) {}

// run invokes the tool and returns its text, an inline skill's prompt.
func run(t *testing.T, tool agentcore.Tool, name, args string) string {
	t.Helper()
	raw, _ := json.Marshal(skillArgs{Skill: name, Args: args})
	text, err := call(t, tool, string(raw))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return text
}

func TestSkillToolExecute(t *testing.T) {
	t.Parallel()

	tool := NewSkillTool(skillCatalog(t, map[string]string{
		"greet": "---\ndescription: Say hello\n---\nHello $ARGUMENTS!",
	}), "test-session", nil, ignoreInvocation)

	text := run(t, tool, "greet", "World")
	if !strings.Contains(text, "Hello World!") || !strings.Contains(text, `<skill name="greet">`) {
		t.Errorf("expected the wrapped, expanded skill, got: %s", text)
	}
}

func TestSkillToolNotFound(t *testing.T) {
	t.Parallel()

	tool := NewSkillTool(skillCatalog(t, nil), "", nil, ignoreInvocation)
	if text := run(t, tool, "nonexistent", ""); !strings.Contains(text, "not found") {
		t.Errorf("expected not-found message, got: %s", text)
	}
}

func TestSkillToolDisableModelInvocation(t *testing.T) {
	t.Parallel()

	tool := NewSkillTool(skillCatalog(t, map[string]string{
		"deploy": "---\ndisable-model-invocation: true\n---\ndeploy stuff",
	}), "", nil, ignoreInvocation)
	if text := run(t, tool, "deploy", ""); !strings.Contains(text, "manual invocation only") {
		t.Errorf("expected manual-only message, got: %s", text)
	}
}

func TestSkillToolContextFork(t *testing.T) {
	t.Parallel()

	var capturedArgs json.RawMessage
	applied := false
	fakeExecutor := func(_ context.Context, args json.RawMessage) (agentcore.Result, error) {
		if !applied {
			t.Fatalf("expected the invocation to be seen before the fork runs")
		}
		capturedArgs = args
		return agentcore.TextResult("research results"), nil
	}

	tool := NewSkillTool(skillCatalog(t, map[string]string{
		"research": "---\ncontext: fork\nagent: explore\nmodel: openai/gpt-5\n---\nResearch $ARGUMENTS",
	}), "", fakeExecutor, func(*skill.Invocation) { applied = true })

	result := run(t, tool, "research", "auth module")

	var params map[string]string
	json.Unmarshal(capturedArgs, &params)
	if params["agent"] != "explore" || params["model"] != "openai/gpt-5" {
		t.Errorf("expected agent=explore and the skill's model, got %v", params)
	}
	if !strings.Contains(params["task"], "Research auth module") {
		t.Errorf("expected expanded task, got %q", params["task"])
	}

	if result != "research results" {
		t.Errorf("expected executor result passthrough, got %s", result)
	}
}

func TestSkillToolContextForkDefaultAgent(t *testing.T) {
	t.Parallel()

	var capturedArgs json.RawMessage
	tool := NewSkillTool(skillCatalog(t, map[string]string{
		"task": "---\ncontext: fork\n---\nDo stuff",
	}), "", func(_ context.Context, args json.RawMessage) (agentcore.Result, error) {
		capturedArgs = args
		return agentcore.TextResult("ok"), nil
	}, ignoreInvocation)

	run(t, tool, "task", "")

	var params map[string]string
	json.Unmarshal(capturedArgs, &params)
	if params["agent"] != "general-purpose" {
		t.Errorf("expected default agent=general-purpose, got %q", params["agent"])
	}
}

func TestSkillToolReportsInvocations(t *testing.T) {
	t.Parallel()

	var seen *skill.Invocation
	tool := NewSkillTool(skillCatalog(t, map[string]string{
		"review": "---\nallowed-tools: bash, read\n---\nreview $ARGUMENTS",
	}), "", nil, func(inv *skill.Invocation) { seen = inv })

	run(t, tool, "review", "diff")
	if seen == nil || !strings.Contains(seen.Prompt, "review diff") || len(seen.AllowedTools) != 2 {
		t.Fatalf("unexpected invocation: %+v", seen)
	}
}
