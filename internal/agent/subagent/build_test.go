package subagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	coresub "github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/tools"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"
)

func mkTools(names ...string) []agentcore.Tool {
	out := make([]agentcore.Tool, 0, len(names))
	for _, n := range names {
		out = append(out, agentcore.Tool{Name: n, Schema: map[string]any{"type": "object"}})
	}
	return out
}

func names(in []agentcore.Tool) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		out = append(out, t.Name)
	}
	return out
}

// mainTools builds the main agent's tools the way the app does.
func mainTools(dir string) []agentcore.Tool {
	w := tools.Workspace{Dir: dir, Files: tools.NewFileReadState()}
	return append([]agentcore.Tool{w.Read(), w.Write(), w.Edit()},
		mkTools("bash", "glob", "grep", "ls", "web_search", "web_fetch", "todo_write", "ask_user")...)
}

func TestToolPoolKeepsTheMainAgentsOwnTools(t *testing.T) {
	got := names(toolPool(BuildDeps{Workspace: tools.Workspace{Dir: "."}, MainTools: mainTools(".")}, &AgentDefinition{Name: "general-purpose"}))
	want := []string{"read", "write", "edit", "bash", "glob", "grep", "ls", "web_search", "web_fetch"}
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestToolPoolAppliesTheDefinition(t *testing.T) {
	deps := BuildDeps{Workspace: tools.Workspace{Dir: "."}, MainTools: mainTools(".")}
	if got := names(toolPool(deps, &AgentDefinition{DisallowedTools: readOnlyDisallowed})); slices.ContainsFunc(got, func(n string) bool {
		return slices.Contains(readOnlyDisallowed, n)
	}) {
		t.Fatalf("read-only agent got %v", got)
	}
	if got := names(toolPool(deps, &AgentDefinition{Tools: []string{"read", "grep", "ask_user"}})); !slices.Equal(got, []string{"read", "grep"}) {
		t.Fatalf("narrowed agent got %v, want [read grep]", got)
	}
	if got := toolPool(deps, &AgentDefinition{Tools: []string{"*"}}); len(got) != 9 {
		t.Fatalf("{*} narrowed the tools to %v", names(got))
	}
}

// A read by one run doesn't let another run or the main agent edit the file.
func TestToolPoolRebuildsTheFileTools(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := mainTools(dir)
	deps := BuildDeps{Workspace: tools.Workspace{Dir: dir}, MainTools: main}
	a, b := toolPool(deps, &AgentDefinition{}), toolPool(deps, &AgentDefinition{})
	run := func(tool agentcore.Tool, args string) error {
		if _, err := tool.Check(context.Background(), json.RawMessage(args)); err != nil {
			return err
		}
		_, err := tool.Run(context.Background(), json.RawMessage(args))
		return err
	}
	if _, err := a[0].Run(context.Background(), json.RawMessage(`{"file_path":"a.txt"}`)); err != nil {
		t.Fatal(err)
	}
	edit := `{"file_path":"a.txt","old_string":"hello","new_string":"bye"}`
	for name, tool := range map[string]agentcore.Tool{"another run": b[2], "the main agent": main[2]} {
		if err := run(tool, edit); err == nil || !strings.Contains(err.Error(), "read") {
			t.Errorf("%s edited what it never read: %v", name, err)
		}
	}
	if err := run(a[2], edit); err != nil {
		t.Fatalf("the run that read it: %v", err)
	}
}

// A run uses the call's model, else the definition's, else the
// conversation's.
func TestAgentConfiguresEachRun(t *testing.T) {
	model := func(name string) agentcore.Model {
		client, err := litellm.New(litellmtest.New())
		if err != nil {
			t.Fatal(err)
		}
		return agentcore.Model{Client: client, Request: litellm.Request{Model: name}}
	}
	var spawned []coresub.Spawn
	deps := BuildDeps{
		Workspace:    tools.Workspace{Dir: "."},
		MainTools:    mainTools("."),
		DefaultModel: model("main"),
		ResolveModel: func(name string) (agentcore.Model, error) { return model(name), nil },
		CompactAt:    1000,
		Emit: func(s coresub.Spawn) func(agentcore.Event) error {
			spawned = append(spawned, s)
			return nil
		},
	}
	for def, want := range map[*AgentDefinition][2]string{
		{Name: "a"}:                   {"main", "fast"},
		{Name: "b", Model: "inherit"}: {"main", "fast"},
		{Name: "c", Model: "small"}:   {"small", "fast"},
	} {
		agent, err := def.Agent(deps)
		if err != nil {
			t.Fatal(err)
		}
		for i, call := range []string{"", "fast"} {
			cfg, err := agent.Config(coresub.Spawn{Agent: def.Name, ID: def.Name + "#1", Model: call})
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Model.Request.Model; got != want[i] || cfg.Compactor == nil || cfg.Cache == nil || len(cfg.Tools) != 9 {
				t.Errorf("%s with %q: model %q, config %+v", def.Name, call, got, cfg)
			}
		}
	}
	if len(spawned) != 6 {
		t.Fatalf("emit asked for %d runs", len(spawned))
	}
}
