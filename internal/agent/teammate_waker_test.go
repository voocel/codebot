package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
	"github.com/voocel/codebot/internal/storage"
)

// recordingSpawner is a stub TeamSpawner that captures every request instead
// of launching a real teammate goroutine.
func recordingSpawner(into *[]subagent.TeamSpawnRequest) subagent.TeamSpawner {
	return func(_ context.Context, req subagent.TeamSpawnRequest) (*subagent.TeamSpawnResult, error) {
		*into = append(*into, req)
		return &subagent.TeamSpawnResult{TaskID: "tm-x", AgentID: req.Name + "@team"}, nil
	}
}

func assistantMsg(text string) agentcore.AgentMessage {
	return agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock(text)},
		StopReason: agentcore.StopReasonStop,
	}
}

func firstText(msgs []agentcore.AgentMessage) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[0].TextContent()
}

// rosterWith returns an in-memory roster store seeded with the given members.
func rosterWith(team string, members ...storage.RosterMember) *storage.RosterStore {
	rs := storage.NewRosterStore()
	rs.SetTeam(team, "")
	for _, m := range members {
		rs.UpsertMember(m)
	}
	return rs
}

func anyTypeConfig(agentType string) (subagent.Config, bool) {
	return subagent.Config{Name: agentType}, true
}

func TestTeammateWaker_WakesWithTranscriptAndRealPrompt(t *testing.T) {
	ts := storage.NewTranscriptStore(filepath.Join(t.TempDir(), "transcripts"))
	if err := ts.Append("researcher", []agentcore.AgentMessage{
		agentcore.UserMsg("investigate the bug"),
		assistantMsg("found it at line 42"),
	}); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}

	roster := rosterWith("alpha", storage.RosterMember{
		Name: "researcher", AgentType: "general-purpose", Color: "blue", Description: "digs", Kind: "teammate",
	})

	var got []subagent.TeamSpawnRequest
	w := NewTeammateWaker(recordingSpawner(&got), anyTypeConfig, nil, roster, ts)

	woke, err := w.Wake(context.Background(), "researcher", "keep digging on the crash")
	if err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if !woke {
		t.Fatal("Wake returned false for a known roster member, want true")
	}
	if len(got) != 1 {
		t.Fatalf("spawner called %d times, want 1", len(got))
	}
	req := got[0]
	if req.Name != "researcher" || req.Color != "blue" || req.Description != "digs" {
		t.Errorf("request identity = {Name:%q Color:%q Desc:%q}, want researcher/blue/digs", req.Name, req.Color, req.Description)
	}
	// The triggering message IS the resume prompt, delivered verbatim.
	if req.InitialPrompt != "keep digging on the crash" {
		t.Errorf("InitialPrompt = %q, want the real message verbatim", req.InitialPrompt)
	}
	// TeamName left empty so the teammate joins the active (resumed) team.
	if req.TeamName != "" {
		t.Errorf("TeamName = %q, want empty (join active team)", req.TeamName)
	}
	if len(req.History) != 2 {
		t.Fatalf("History = %d messages, want 2 (the seeded transcript)", len(req.History))
	}
	if req.History[0].TextContent() != "investigate the bug" || req.History[1].TextContent() != "found it at line 42" {
		t.Errorf("History content mismatch: %q / %q", req.History[0].TextContent(), req.History[1].TextContent())
	}
}

func TestTeammateWaker_UnknownNameIsNoOp(t *testing.T) {
	var got []subagent.TeamSpawnRequest
	w := NewTeammateWaker(recordingSpawner(&got), anyTypeConfig, nil, rosterWith("alpha"), nil)

	woke, err := w.Wake(context.Background(), "ghost", "hello?")
	if err != nil {
		t.Fatalf("unknown name should be a silent no-op, got err %v", err)
	}
	if woke {
		t.Error("Wake returned true for an unknown name, want false (caller falls through)")
	}
	if len(got) != 0 {
		t.Errorf("spawner should not be called for an unknown name, got %d calls", len(got))
	}
}

func TestTeammateWaker_UnknownAgentTypeErrors(t *testing.T) {
	roster := rosterWith("alpha", storage.RosterMember{Name: "ghost", AgentType: "deleted-type"})
	var got []subagent.TeamSpawnRequest
	configOf := func(agentType string) (subagent.Config, bool) {
		return subagent.Config{Name: agentType}, agentType == "known"
	}
	w := NewTeammateWaker(recordingSpawner(&got), configOf, nil, roster, nil)

	woke, err := w.Wake(context.Background(), "ghost", "come back")
	if err == nil {
		t.Error("expected an error for a member whose agent type no longer exists")
	}
	if woke {
		t.Error("Wake returned true despite the failure")
	}
	if len(got) != 0 {
		t.Errorf("spawner should not be called when the agent type is unknown, got %d", len(got))
	}
}

func TestTeammateWaker_NilDepsAreNoOp(t *testing.T) {
	var w *TeammateWaker // nil receiver
	if woke, err := w.Wake(context.Background(), "x", "y"); woke || err != nil {
		t.Errorf("nil waker = (%v, %v), want (false, nil)", woke, err)
	}
}

// A roster member whose name is ALREADY live in the registry must not be
// re-spawned — that would clone the teammate. Wake reports not-spawned so the
// caller delivers to the existing mailbox instead. This is the deterministic
// stand-in for the concurrent double-wake the lock + re-check guards against.
func TestTeammateWaker_LiveNameNotRespawned(t *testing.T) {
	reg := team.NewRegistry()
	if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if err := reg.RegisterAgent("researcher", "tm-1"); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	roster := rosterWith("alpha", storage.RosterMember{Name: "researcher", AgentType: "general-purpose"})

	var got []subagent.TeamSpawnRequest
	w := NewTeammateWaker(recordingSpawner(&got), anyTypeConfig, reg, roster, nil)

	woke, err := w.Wake(context.Background(), "researcher", "second message")
	if err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if woke {
		t.Error("Wake returned true for an already-live name, want false (no clone)")
	}
	if len(got) != 0 {
		t.Errorf("spawner called %d times for a live teammate, want 0 (must not clone)", len(got))
	}
}

// End-to-end seam: a real spawn (via TeammateSpawner) must write the roster +
// transcript to disk, and a fresh "restart" (new registry, reloaded stores)
// must wake the teammate ON DEMAND — seeded with that persisted transcript and
// the triggering message as its opening turn. This is the P3↔wake contract the
// isolated tests don't cover together.
func TestTeammateSpawner_PersistThenWakeSeam(t *testing.T) {
	dir := t.TempDir()
	rosterStore := storage.NewRosterStore()
	if err := rosterStore.SetDir(dir); err != nil {
		t.Fatalf("roster SetDir: %v", err)
	}
	transcripts := storage.NewTranscriptStore(filepath.Join(dir, "transcripts"))

	reg := team.NewRegistry()
	rt := task.NewRuntime()
	if err := reg.CreateTeam("alpha", "the team", "leader-1"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	cfg := subagent.Config{Name: "researcher", Model: newScriptModel("found it at line 42")}
	spawner := TeammateSpawner(reg, rt, nil, nil, nil, nil, team.ProtocolHooks{}, nil,
		&TeammatePersist{Roster: rosterStore, Transcripts: transcripts})

	res, err := spawner(context.Background(), subagent.TeamSpawnRequest{
		Config:        cfg,
		Name:          "alice",
		TeamName:      "alpha",
		InitialPrompt: "investigate the crash",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// Wait for the opening turn to complete and the teammate to park idle —
	// the transcript Append runs inside the turn, so by idle it is on disk.
	waitFor(t, 2*time.Second, func() bool {
		e := rt.Get(res.TaskID)
		return e != nil && e.IsIdle
	})
	_ = reg.DeleteTeam()
	waitFor(t, time.Second, func() bool {
		e := rt.Get(res.TaskID)
		return e != nil && e.Status.IsTerminal()
	})

	// Roster persisted the live member; transcript captured the turn.
	snap := rosterStore.Snapshot()
	if snap.Team != "alpha" || len(snap.Members) != 1 || snap.Members[0].Name != "alice" || snap.Members[0].AgentType != "researcher" {
		t.Fatalf("roster after spawn = %+v, want team alpha with member alice(researcher)", snap)
	}
	hist, err := transcripts.Load("alice")
	if err != nil {
		t.Fatalf("load transcript: %v", err)
	}
	if len(hist) < 2 || hist[0].TextContent() != "investigate the crash" {
		t.Fatalf("transcript = %d msgs (head %q), want >=2 starting with the prompt", len(hist), firstText(hist))
	}

	// --- Simulate restart: reload stores, wake alice through a stub spawner.
	rosterStore2 := storage.NewRosterStore()
	if err := rosterStore2.SetDir(dir); err != nil {
		t.Fatalf("reload roster: %v", err)
	}
	transcripts2 := storage.NewTranscriptStore(filepath.Join(dir, "transcripts"))

	var got []subagent.TeamSpawnRequest
	w := NewTeammateWaker(recordingSpawner(&got), anyTypeConfig, nil, rosterStore2, transcripts2)
	woke, err := w.Wake(context.Background(), "alice", "resume now — what's the status?")
	if err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if !woke || len(got) != 1 || got[0].Name != "alice" {
		t.Fatalf("wake spawned %d teammates (woke=%v), want 1 (alice): %+v", len(got), woke, got)
	}
	if got[0].InitialPrompt != "resume now — what's the status?" {
		t.Errorf("InitialPrompt = %q, want the triggering message", got[0].InitialPrompt)
	}
	if len(got[0].History) < 2 || got[0].History[0].TextContent() != "investigate the crash" {
		t.Errorf("woken History = %d msgs (head %q), want the persisted transcript", len(got[0].History), firstText(got[0].History))
	}
}
