package tools

import (
	"strings"
	"testing"
)

func TestEnterWorktree_PassesNameAndReports(t *testing.T) {
	var gotName string
	tool := NewEnterWorktree(func(name string) (string, error) {
		gotName = name
		return "/repo/.worktrees/feat", nil
	})

	out, err := call(t, tool, `{"name":"feat"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotName != "feat" {
		t.Errorf("enter received name %q, want %q", gotName, "feat")
	}
	if !strings.Contains(out, "/repo/.worktrees/feat") {
		t.Errorf("result %q does not name the sandbox dir", out)
	}
}

func TestEnterWorktree_NoArgsIsAllowed(t *testing.T) {
	tool := NewEnterWorktree(func(name string) (string, error) {
		if name != "" {
			t.Errorf("expected empty name for random, got %q", name)
		}
		return "/repo/.worktrees/random", nil
	})
	if _, err := call(t, tool, `{}`); err != nil {
		t.Fatalf("Run with no args should succeed: %v", err)
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
		tool := NewExitWorktree(func(discard bool) (string, error) {
			gotDiscard = discard
			return "left worktree", nil
		})
		if _, err := call(t, tool, `{"action":"`+tc.action+`"}`); err != nil {
			t.Fatalf("action %q: %v", tc.action, err)
		}
		if gotDiscard != tc.wantDiscard {
			t.Errorf("action %q → discard=%v, want %v", tc.action, gotDiscard, tc.wantDiscard)
		}
	}
}

func TestExitWorktree_InvalidAction(t *testing.T) {
	tool := NewExitWorktree(func(bool) (string, error) { return "", nil })
	if _, err := call(t, tool, `{"action":"remove"}`); err == nil {
		t.Fatal("invalid action should error")
	}
	if _, err := call(t, tool, `{}`); err == nil {
		t.Fatal("missing action should error")
	}
}
