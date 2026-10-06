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

// skillCatalog loads the skill files given, by name, as privileged skills.
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
		specs[i].Privileged = true
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

// The model cannot invoke a skill kept for manual invocation, nor one that
// does not exist; it is told so in text.
func TestSkillToolDisableModelInvocation(t *testing.T) {
	t.Parallel()

	tool := NewSkillTool(skillCatalog(t, map[string]string{
		"deploy": "---\ndisable-model-invocation: true\n---\ndeploy stuff",
	}), "", nil, ignoreInvocation)
	if text := run(t, tool, "deploy", ""); !strings.Contains(text, "manual invocation only") {
		t.Errorf("expected manual-only message, got: %s", text)
	}
	if text := run(t, tool, "nonexistent", ""); !strings.Contains(text, "not found") {
		t.Errorf("expected not-found message, got: %s", text)
	}
}

// A forked skill runs as a sub-agent once its invocation is seen, with the
// skill's agent and model, or the general-purpose agent when it names none.
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
		"task":     "---\ncontext: fork\n---\nDo stuff",
	}), "", fakeExecutor, func(*skill.Invocation) { applied = true })
	fork := func(name, args string) (string, map[string]string) {
		t.Helper()
		result := run(t, tool, name, args)
		var params map[string]string
		json.Unmarshal(capturedArgs, &params)
		return result, params
	}

	result, params := fork("research", "auth module")
	if params["agent"] != "explore" || params["model"] != "openai/gpt-5" {
		t.Errorf("expected agent=explore and the skill's model, got %v", params)
	}
	if !strings.Contains(params["task"], "Research auth module") {
		t.Errorf("expected expanded task, got %q", params["task"])
	}
	if result != "research results" {
		t.Errorf("expected executor result passthrough, got %s", result)
	}

	if _, params := fork("task", ""); params["agent"] != "general-purpose" {
		t.Errorf("expected default agent=general-purpose, got %q", params["agent"])
	}
}

// An inline skill returns its prompt, and the invocation, the source of the
// conversation's grants, reaches the caller with the tools it allows.
func TestSkillToolReportsInvocations(t *testing.T) {
	t.Parallel()

	var seen *skill.Invocation
	tool := NewSkillTool(skillCatalog(t, map[string]string{
		"review": "---\nallowed-tools: bash, read\n---\nreview $ARGUMENTS",
	}), "", nil, func(inv *skill.Invocation) { seen = inv })

	text := run(t, tool, "review", "diff")
	if seen == nil || !strings.Contains(seen.Prompt, "review diff") || len(seen.AllowedTools) != 2 {
		t.Fatalf("unexpected invocation: %+v", seen)
	}
	if text != seen.Prompt {
		t.Errorf("returned %q, want the invocation's prompt", text)
	}
}
