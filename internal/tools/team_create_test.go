package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore/team"
	cbteam "github.com/voocel/codebot/internal/team"
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

// When the team is empty (no teammates yet) a second team_create call is
// treated as a rename — that's the whole point of the tool now that bootstrap
// pre-creates a default team.
func TestTeamCreate_RenamesEmptyTeam(t *testing.T) {
	reg := team.NewRegistry()
	tool := NewTeamCreateTool(reg, "s1")

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"alpha"}`)); err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"beta"}`))
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, string(out))
	}
	if resp["success"] != true {
		t.Errorf("expected success=true, got %+v", resp)
	}
	if resp["team_name"] != "beta" {
		t.Errorf("team_name = %v, want beta", resp["team_name"])
	}
	if ctx := reg.Team(); ctx == nil || ctx.Name != "beta" {
		t.Errorf("registry team = %+v, want beta", ctx)
	}
	// Coming from a model-chosen name: the wording is the literal rename so
	// the model sees what it actually did.
	if msg, _ := resp["message"].(string); !strings.Contains(msg, "renamed from") {
		t.Errorf("message = %q, want 'renamed from' wording", msg)
	}
}

// The most common bootstrap path: registry starts on "default", model calls
// team_create with a meaningful name. From the model's point of view that
// IS the team's creation event — the result message must say "created"
// rather than leak the "default" implementation detail.
func TestTeamCreate_HidesDefaultRenameAsCreation(t *testing.T) {
	reg := team.NewRegistry()
	// Mirror bootstrap: pre-create the default team before the tool fires.
	if err := reg.CreateTeam(cbteam.DefaultTeamName, "", "s1"); err != nil {
		t.Fatalf("CreateTeam default: %v", err)
	}
	tool := NewTeamCreateTool(reg, "s1")

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"alpha"}`))
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
	msg, _ := resp["message"].(string)
	if !strings.Contains(msg, "created") {
		t.Errorf("message = %q, want 'created' wording", msg)
	}
	if strings.Contains(msg, cbteam.DefaultTeamName) {
		t.Errorf("message %q must not leak %q to the model", msg, cbteam.DefaultTeamName)
	}
}

// Calling team_create with the same name should not error — it's a no-op
// confirmation, which the model occasionally does at session start.
func TestTeamCreate_SameNameIsNoop(t *testing.T) {
	reg := team.NewRegistry()
	tool := NewTeamCreateTool(reg, "s1")

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"alpha"}`)); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"alpha"}`))
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, string(out))
	}
	if resp["success"] != true {
		t.Errorf("expected success=true, got %+v", resp)
	}
}

// Once a teammate is registered the rename must be rejected — teammate
// agent IDs embed the team name at spawn time, so renaming would silently
// invalidate them.
func TestTeamCreate_RejectsRenameWithTeammates(t *testing.T) {
	reg := team.NewRegistry()
	tool := NewTeamCreateTool(reg, "s1")

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"alpha"}`)); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if err := reg.RegisterAgent("researcher", "tm-1"); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"team_name":"beta"}`))
	if err != nil {
		t.Fatalf("rename Execute: %v", err)
	}
	var msg string
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, string(out))
	}
	if !strings.Contains(msg, "Cannot rename") {
		t.Errorf("error = %q, want 'Cannot rename'", msg)
	}
	if ctx := reg.Team(); ctx == nil || ctx.Name != "alpha" {
		t.Errorf("registry team = %+v, want alpha (unchanged)", ctx)
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
