package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/agentcore/team"
	cbteam "github.com/voocel/codebot/internal/team"
)

func newDismissHarness(t *testing.T) (*TeamDismissTool, *team.Registry) {
	t.Helper()
	reg := team.NewRegistry()
	return NewTeamDismissTool(reg), reg
}

func execDismiss(t *testing.T, tool *TeamDismissTool, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	var s string
	if err := json.Unmarshal(out, &s); err == nil {
		return s
	}
	return string(out)
}

func TestTeamDismiss_QueuesShutdownEnvelope(t *testing.T) {
	tool, reg := newDismissHarness(t)
	if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if err := reg.RegisterAgent("alice", "tm-1"); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	execDismiss(t, tool, map[string]any{"name": "alice", "reason": "task done"})

	msgs := reg.Mailbox("alice").Drain()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 mailbox message, got %d", len(msgs))
	}
	if !cbteam.IsShutdownRequest(msgs[0].Text) {
		t.Errorf("payload not recognised as shutdown_request: %q", msgs[0].Text)
	}
	if msgs[0].From != team.TeamLeadName {
		t.Errorf("From = %q, want %q", msgs[0].From, team.TeamLeadName)
	}
}

func TestTeamDismiss_ValidationErrors(t *testing.T) {
	tool, reg := newDismissHarness(t)
	if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if err := reg.RegisterAgent("alice", "tm-1"); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	cases := []struct {
		name   string
		args   map[string]any
		expect string
	}{
		{"empty name", map[string]any{"name": ""}, "name is required"},
		{"leader name", map[string]any{"name": team.TeamLeadName}, "Cannot dismiss"},
		{"unknown name", map[string]any{"name": "ghost"}, `No teammate named "ghost"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := execDismiss(t, tool, tc.args)
			if !strings.Contains(got, tc.expect) {
				t.Errorf("got %q, want substring %q", got, tc.expect)
			}
		})
	}
}

func TestTeamDismiss_NoTeam(t *testing.T) {
	tool, _ := newDismissHarness(t)
	got := execDismiss(t, tool, map[string]any{"name": "alice"})
	if !strings.Contains(got, "No active team") {
		t.Errorf("expected no-team message, got %q", got)
	}
}

func TestTeamDismiss_AlreadyClosedMailbox(t *testing.T) {
	tool, reg := newDismissHarness(t)
	if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if err := reg.RegisterAgent("alice", "tm-1"); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	reg.Mailbox("alice").Close()

	got := execDismiss(t, tool, map[string]any{"name": "alice"})
	if !strings.Contains(got, "already shut down") {
		t.Errorf("expected already-shut-down message, got %q", got)
	}
}
