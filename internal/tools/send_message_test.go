package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
)

// sendMessageHarness wires together the pieces a SendMessageTool needs and
// hands back the tool plus the underlying registry/runtime so tests can
// inspect routing outcomes.
type sendMessageHarness struct {
	tool *SendMessageTool
	reg  *team.Registry
	rt   *task.Runtime
}

func newSendMessageHarness(t *testing.T) *sendMessageHarness {
	t.Helper()
	reg := team.NewRegistry()
	rt := task.NewRuntime()
	return &sendMessageHarness{
		tool: NewSendMessageTool(rt, reg),
		reg:  reg,
		rt:   rt,
	}
}

// withTeam activates a team with the leader pre-registered. Use this in tests
// that route to teammates; subagent-only tests don't need it.
func (h *sendMessageHarness) withTeam(t *testing.T, name string) *sendMessageHarness {
	t.Helper()
	if err := h.reg.CreateTeam(name, "", "leader-task"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	return h
}

func (h *sendMessageHarness) addTeammate(t *testing.T, name, taskID string) {
	t.Helper()
	if err := h.reg.RegisterAgent(name, taskID); err != nil {
		t.Fatalf("RegisterAgent(%s): %v", name, err)
	}
	h.rt.Register(&task.Entry{
		ID:        taskID,
		Type:      task.TypeTeammate,
		Status:    task.Running,
		StartedAt: time.Now(),
		Identity:  &task.Identity{AgentName: name},
	})
}

func (h *sendMessageHarness) addSubAgent(t *testing.T, id string) {
	t.Helper()
	h.rt.Register(&task.Entry{
		ID:        id,
		Type:      task.TypeSubAgent,
		Status:    task.Running,
		StartedAt: time.Now(),
		Agent:     "explore",
	})
}

func mustExecute(t *testing.T, tool *SendMessageTool, ctx context.Context, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	out, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	// Result is always a JSON string (success object or plain validation text).
	var s string
	if err := json.Unmarshal(out, &s); err == nil {
		return s
	}
	return string(out)
}

func TestSendMessage_ValidationErrors(t *testing.T) {
	h := newSendMessageHarness(t)
	cases := []struct {
		name   string
		args   map[string]any
		expect string
	}{
		{"empty to", map[string]any{"to": "", "message": "hi"}, "to is required"},
		{"empty message", map[string]any{"to": "anyone", "message": "   "}, "message is required"},
		{"unknown recipient", map[string]any{"to": "ghost", "message": "hi"}, `No agent or task matches "ghost"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustExecute(t, h.tool, context.Background(), tc.args)
			if !strings.Contains(got, tc.expect) {
				t.Errorf("got %q, want substring %q", got, tc.expect)
			}
		})
	}
}

func TestSendMessage_RoutesToTeammateMailbox(t *testing.T) {
	h := newSendMessageHarness(t).withTeam(t, "alpha")
	h.addTeammate(t, "researcher", "tm-1")

	mustExecute(t, h.tool, context.Background(), map[string]any{
		"to":      "researcher",
		"message": "find the bug",
	})

	mb := h.reg.Mailbox("researcher")
	if mb == nil {
		t.Fatal("researcher mailbox missing")
	}
	msgs := mb.Drain()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 mailbox message, got %d", len(msgs))
	}
	if msgs[0].Text != "find the bug" {
		t.Errorf("message text = %q, want %q", msgs[0].Text, "find the bug")
	}
	// Sender defaults to team-lead when no identity is in ctx.
	if msgs[0].From != team.TeamLeadName {
		t.Errorf("sender = %q, want %q (default for ctx without identity)", msgs[0].From, team.TeamLeadName)
	}
}

func TestSendMessage_SenderIdentityFromContext(t *testing.T) {
	h := newSendMessageHarness(t).withTeam(t, "alpha")
	h.addTeammate(t, "researcher", "tm-1")
	h.addTeammate(t, "tester", "tm-2")

	ctx := team.WithIdentity(context.Background(), &task.Identity{
		AgentName: "tester",
		Color:     "green",
	})
	mustExecute(t, h.tool, ctx, map[string]any{
		"to":      "researcher",
		"message": "your turn",
	})

	msgs := h.reg.Mailbox("researcher").Drain()
	if len(msgs) != 1 || msgs[0].From != "tester" || msgs[0].Color != "green" {
		t.Fatalf("expected one message from tester (green), got %+v", msgs)
	}
}

func TestSendMessage_RejectsSelfSend(t *testing.T) {
	h := newSendMessageHarness(t).withTeam(t, "alpha")
	h.addTeammate(t, "researcher", "tm-1")

	ctx := team.WithIdentity(context.Background(), &task.Identity{AgentName: "researcher"})
	got := mustExecute(t, h.tool, ctx, map[string]any{
		"to":      "researcher",
		"message": "talking to myself",
	})
	if !strings.Contains(got, "cannot send a message to yourself") {
		t.Errorf("expected self-send rejection, got %q", got)
	}
	if depth := h.reg.Mailbox("researcher").Len(); depth != 0 {
		t.Errorf("self-send should not enqueue, mailbox depth = %d", depth)
	}
}

func TestSendMessage_LeaderRoutesToTeamLead(t *testing.T) {
	h := newSendMessageHarness(t).withTeam(t, "alpha")
	h.addTeammate(t, "tester", "tm-2")

	// Teammate addressing "team-lead" — should land in leader mailbox.
	ctx := team.WithIdentity(context.Background(), &task.Identity{AgentName: "tester"})
	mustExecute(t, h.tool, ctx, map[string]any{
		"to":      team.TeamLeadName,
		"message": "all done",
	})

	msgs := h.reg.Mailbox(team.TeamLeadName).Drain()
	if len(msgs) != 1 || msgs[0].From != "tester" || msgs[0].Text != "all done" {
		t.Fatalf("expected leader mailbox to receive tester's message, got %+v", msgs)
	}
}

func TestSendMessage_RoutesToSubAgentByID(t *testing.T) {
	h := newSendMessageHarness(t)
	h.addSubAgent(t, "bg-3")

	got := mustExecute(t, h.tool, context.Background(), map[string]any{
		"to":      "bg-3",
		"message": "extra context",
	})
	if !strings.Contains(got, "Message queued for subagent bg-3") {
		t.Errorf("expected subagent-queued message, got %q", got)
	}
}

func TestSendMessage_RejectsTerminalSubAgent(t *testing.T) {
	h := newSendMessageHarness(t)
	h.rt.Register(&task.Entry{
		ID:        "bg-4",
		Type:      task.TypeSubAgent,
		Status:    task.Completed,
		StartedAt: time.Now(),
		EndedAt:   time.Now(),
		Agent:     "explore",
	})

	got := mustExecute(t, h.tool, context.Background(), map[string]any{
		"to":      "bg-4",
		"message": "too late",
	})
	if !strings.Contains(got, "already finished") {
		t.Errorf("expected terminal-rejection message, got %q", got)
	}
}

func TestSendMessage_ClosedTeammateMailbox(t *testing.T) {
	h := newSendMessageHarness(t).withTeam(t, "alpha")
	h.addTeammate(t, "researcher", "tm-1")
	// Simulate teammate shutdown by closing its mailbox directly. UnregisterAgent
	// would remove the name from the registry, which is a different failure mode
	// (resolution would fall through to subagent lookup). Closing instead leaves
	// the name resolvable but the channel dead — the exact state right after a
	// graceful shutdown but before unregister.
	h.reg.Mailbox("researcher").Close()

	got := mustExecute(t, h.tool, context.Background(), map[string]any{
		"to":      "researcher",
		"message": "anyone home",
	})
	if !strings.Contains(got, "shut down") {
		t.Errorf("expected closed-mailbox message, got %q", got)
	}
}

// fakeWaker records Wake calls and returns a scripted outcome, standing in for
// internal/agent.TeammateWaker so send_message routing can be tested without a
// real spawn path.
type fakeWaker struct {
	woke   bool
	err    error
	calls  int
	gotTo  string
	gotMsg string
}

func (f *fakeWaker) Wake(_ context.Context, name, prompt string) (bool, error) {
	f.calls++
	f.gotTo, f.gotMsg = name, prompt
	return f.woke, f.err
}

func TestSendMessage_WakesDormantTeammate(t *testing.T) {
	h := newSendMessageHarness(t).withTeam(t, "alpha")
	// No live teammate named "researcher" — only the leader is registered, so
	// the name is unresolved in the registry and the waker is consulted.
	fw := &fakeWaker{woke: true}
	h.tool.SetWaker(fw)

	got := mustExecute(t, h.tool, context.Background(), map[string]any{"to": "researcher", "message": "resume: status?"})
	if fw.calls != 1 || fw.gotTo != "researcher" || fw.gotMsg != "resume: status?" {
		t.Fatalf("waker invoked %d times with (to=%q msg=%q), want 1 (researcher / resume: status?)", fw.calls, fw.gotTo, fw.gotMsg)
	}
	if !strings.Contains(got, "Resumed teammate researcher") {
		t.Errorf("got %q, want a resume-success message", got)
	}
}

func TestSendMessage_LiveTeammateNotWoken(t *testing.T) {
	h := newSendMessageHarness(t).withTeam(t, "alpha")
	h.addTeammate(t, "researcher", "tm-1")
	fw := &fakeWaker{woke: true}
	h.tool.SetWaker(fw)

	_ = mustExecute(t, h.tool, context.Background(), map[string]any{"to": "researcher", "message": "hi"})
	if fw.calls != 0 {
		t.Errorf("waker called %d times for a LIVE teammate, want 0 (must route to its mailbox)", fw.calls)
	}
	if depth := h.reg.Mailbox("researcher").Len(); depth != 1 {
		t.Errorf("live teammate mailbox depth = %d, want 1 (message delivered, not woken)", depth)
	}
}

func TestSendMessage_WakerDeclineFallsThrough(t *testing.T) {
	h := newSendMessageHarness(t).withTeam(t, "alpha")
	h.tool.SetWaker(&fakeWaker{woke: false}) // name is not a known dormant teammate

	got := mustExecute(t, h.tool, context.Background(), map[string]any{"to": "ghost", "message": "hi"})
	if !strings.Contains(got, `No agent or task matches "ghost"`) {
		t.Errorf("got %q, want fall-through to not-found", got)
	}
}

func TestSendMessage_WakerErrorSurfaces(t *testing.T) {
	h := newSendMessageHarness(t).withTeam(t, "alpha")
	h.tool.SetWaker(&fakeWaker{err: errors.New(`unknown agent type "x"`)})

	got := mustExecute(t, h.tool, context.Background(), map[string]any{"to": "researcher", "message": "hi"})
	if !strings.Contains(got, "Could not resume teammate") {
		t.Errorf("got %q, want a resume-failure message", got)
	}
}
