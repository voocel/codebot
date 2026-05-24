package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore/team"
)

func TestTeamCreate_CreatesTeamAndRegistersLeader(t *testing.T) {
	reg := team.NewRegistry()
	tool := NewTeamCreateTool(reg, "session-xyz")

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"alpha","description":"test team"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, string(out))
	}
	if resp["success"] != true {
		t.Errorf("expected success=true, got %+v", resp)
	}
	if resp["team_name"] != "alpha" {
		t.Errorf("team_name = %v, want alpha", resp["team_name"])
	}
	if resp["leader_name"] != team.TeamLeadName {
		t.Errorf("leader_name = %v, want %q", resp["leader_name"], team.TeamLeadName)
	}

	if !reg.HasTeam() {
		t.Fatal("registry has no team after CreateTeam")
	}
	if reg.Mailbox(team.TeamLeadName) == nil {
		t.Error("leader mailbox not created")
	}
	if id, ok := reg.TaskID(team.TeamLeadName); !ok || id != "session-xyz" {
		t.Errorf("leader TaskID = (%q, %v), want (session-xyz, true)", id, ok)
	}
}

func TestTeamCreate_ValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{"empty name", `{"team_name":""}`, "team_name is required"},
		{"whitespace name", `{"team_name":"   "}`, "team_name is required"},
		{"@ in name", `{"team_name":"a@b"}`, "must start with"},
		{"space in name", `{"team_name":"hello world"}`, "must start with"},
		{"leading dot", `{"team_name":".hidden"}`, "must start with"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := team.NewRegistry()
			tool := NewTeamCreateTool(reg, "s1")
			out, err := tool.Execute(context.Background(), json.RawMessage(tt.args))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			var msg string
			if err := json.Unmarshal(out, &msg); err != nil {
				t.Fatalf("unmarshal: %v: %s", err, string(out))
			}
			if !strings.Contains(msg, tt.want) {
				t.Errorf("error = %q, want substring %q", msg, tt.want)
			}
			if reg.HasTeam() {
				t.Error("invalid input still created a team")
			}
		})
	}
}

func TestTeamCreate_RejectsDuplicate(t *testing.T) {
	reg := team.NewRegistry()
	tool := NewTeamCreateTool(reg, "s1")

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"alpha"}`)); err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"beta"}`))
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	var msg string
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, string(out))
	}
	if !strings.Contains(msg, "already active") {
		t.Errorf("duplicate error = %q, want 'already active'", msg)
	}

	// Registry still has the original team, not the second attempt.
	ctx := reg.Team()
	if ctx == nil || ctx.Name != "alpha" {
		t.Errorf("registry team = %+v, want alpha", ctx)
	}
}

func TestTeamCreate_NameWithAllowedChars(t *testing.T) {
	cases := []string{
		"alpha",
		"alpha-beta",
		"alpha_v2",
		"team.dev",
		"a1b2c3",
		"X",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			reg := team.NewRegistry()
			tool := NewTeamCreateTool(reg, "s1")
			args, _ := json.Marshal(map[string]string{"team_name": name})
			out, err := tool.Execute(context.Background(), args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			var resp map[string]any
			if err := json.Unmarshal(out, &resp); err != nil {
				t.Fatalf("unmarshal: %v: %s", err, string(out))
			}
			if resp["success"] != true {
				t.Errorf("team_name %q rejected: %+v", name, resp)
			}
		})
	}
}
