package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEnterWorktree_PassesNameAndReports(t *testing.T) {
	var gotName string
	tool := NewEnterWorktree()
	tool.SetEnter(func(name string) (string, error) {
		gotName = name
		return "/repo/.worktrees/feat", nil
	})

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"feat"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotName != "feat" {
		t.Errorf("enter received name %q, want %q", gotName, "feat")
	}
	var res map[string]any
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res["worktree_path"] != "/repo/.worktrees/feat" {
		t.Errorf("result worktree_path = %v, want the sandbox dir", res["worktree_path"])
	}
}

func TestEnterWorktree_NoArgsIsAllowed(t *testing.T) {
	tool := NewEnterWorktree()
	tool.SetEnter(func(name string) (string, error) {
		if name != "" {
			t.Errorf("expected empty name for random, got %q", name)
		}
		return "/repo/.worktrees/random", nil
	})
	if _, err := tool.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute with no args should succeed: %v", err)
	}
}

func TestEnterWorktree_NotWired(t *testing.T) {
	if _, err := NewEnterWorktree().Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("unwired enter_worktree should error")
	}
}

func TestExitWorktree_ActionMapsToDiscard(t *testing.T) {
	for _, tc := range []struct {
		action      string
		wantDiscard bool
	}{
		{"keep", false},
		{"discard", true},
	} {
		var gotDiscard bool
		tool := NewExitWorktree()
		tool.SetExit(func(discard bool) (string, error) {
			gotDiscard = discard
			return "left worktree", nil
		})
		args := json.RawMessage(`{"action":"` + tc.action + `"}`)
		if _, err := tool.Execute(context.Background(), args); err != nil {
			t.Fatalf("action %q: %v", tc.action, err)
		}
		if gotDiscard != tc.wantDiscard {
			t.Errorf("action %q → discard=%v, want %v", tc.action, gotDiscard, tc.wantDiscard)
		}
	}
}

func TestExitWorktree_InvalidAction(t *testing.T) {
	tool := NewExitWorktree()
	tool.SetExit(func(bool) (string, error) { return "", nil })
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"remove"}`)); err == nil {
		t.Fatal("invalid action should error")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing action should error")
	}
}

func TestExitWorktree_NotWired(t *testing.T) {
	if _, err := NewExitWorktree().Execute(context.Background(), json.RawMessage(`{"action":"keep"}`)); err == nil {
		t.Fatal("unwired exit_worktree should error")
	}
}
