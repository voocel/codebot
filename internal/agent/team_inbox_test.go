package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/team"
	cbteam "github.com/voocel/codebot/internal/team"
)

// fakeInjector records every message handed to Inject so the test can assert
// what crossed the mailbox → agent boundary. Inject results are uniform —
// the pump ignores them anyway — so we don't bother varying them per call.
type fakeInjector struct {
	mu       sync.Mutex
	received []agentcore.AgentMessage
	wake     chan struct{} // closed once on first Inject, allows ordered tests
	once     sync.Once
}

func newFakeInjector() *fakeInjector {
	return &fakeInjector{wake: make(chan struct{})}
}

func (f *fakeInjector) Inject(_ context.Context, m agentcore.AgentMessage) (agentcore.InjectResult, error) {
	f.mu.Lock()
	f.received = append(f.received, m)
	f.mu.Unlock()
	f.once.Do(func() { close(f.wake) })
	return agentcore.InjectResult{Disposition: agentcore.InjectQueued}, nil
}

func (f *fakeInjector) snapshot() []agentcore.AgentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]agentcore.AgentMessage, len(f.received))
	copy(out, f.received)
	return out
}

// waitForCount polls until len(received) >= n or the deadline expires. Returns
// the snapshot so the caller can inspect contents without re-locking.
func (f *fakeInjector) waitForCount(t *testing.T, n int, within time.Duration) []agentcore.AgentMessage {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		snap := f.snapshot()
		if len(snap) >= n {
			return snap
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected at least %d injected messages within %s, got %d", n, within, len(f.snapshot()))
	return nil
}

// startPump spins up a LeaderInboxPump on its own goroutine with a tight
// wait interval so pre-team backoff loops don't dominate test latency.
func startPump(t *testing.T, reg *team.Registry, inj *fakeInjector) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	pump := NewLeaderInboxPump(reg, inj, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pump.Run(ctx)
		close(done)
	}()
	return cancel, done
}

func TestLeaderInboxPump_InjectsPeerMessages(t *testing.T) {
	reg := team.NewRegistry()
	if err := reg.CreateTeam("alpha", "", "sess-1"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	inj := newFakeInjector()
	cancel, done := startPump(t, reg, inj)
	defer func() {
		cancel()
		<-done
	}()

	mb := reg.Mailbox(team.TeamLeadName)
	if mb == nil {
		t.Fatal("leader mailbox missing after CreateTeam")
	}
	if err := mb.Send(team.Message{From: "researcher", Text: "found a clue", Color: "blue"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := inj.waitForCount(t, 1, time.Second)
	body := got[0].TextContent()
	if !strings.Contains(body, `teammate_id="researcher"`) {
		t.Errorf("injected message missing teammate_id attribute: %q", body)
	}
	if !strings.Contains(body, "found a clue") {
		t.Errorf("injected message missing body text: %q", body)
	}
}

func TestLeaderInboxPump_SurfacesIdleWithText(t *testing.T) {
	reg := team.NewRegistry()
	if err := reg.CreateTeam("alpha", "", "sess-1"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	inj := newFakeInjector()
	cancel, done := startPump(t, reg, inj)
	defer func() {
		cancel()
		<-done
	}()

	mb := reg.Mailbox(team.TeamLeadName)
	idle := `{"type":"idle_notification","from":"researcher","text":"I found the bug at line 42."}`
	if err := mb.Send(team.Message{From: "researcher", Text: idle}); err != nil {
		t.Fatalf("Send idle: %v", err)
	}

	got := inj.waitForCount(t, 1, time.Second)
	body := got[0].TextContent()
	if !strings.Contains(body, "I found the bug at line 42.") {
		t.Errorf("expected last-assistant text to surface, got %q", body)
	}
	if !strings.Contains(body, `teammate_id="researcher"`) {
		t.Errorf("expected teammate_id attribute, got %q", body)
	}
}

func TestLeaderInboxPump_FallsBackForIdleWithoutText(t *testing.T) {
	reg := team.NewRegistry()
	if err := reg.CreateTeam("alpha", "", "sess-1"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	inj := newFakeInjector()
	cancel, done := startPump(t, reg, inj)
	defer func() {
		cancel()
		<-done
	}()

	mb := reg.Mailbox(team.TeamLeadName)
	idle := `{"type":"idle_notification","from":"researcher"}`
	if err := mb.Send(team.Message{From: "researcher", Text: idle}); err != nil {
		t.Fatalf("Send idle: %v", err)
	}

	got := inj.waitForCount(t, 1, time.Second)
	body := got[0].TextContent()
	if !strings.Contains(body, "researcher is idle") {
		t.Errorf("expected fallback status line, got %q", body)
	}
}

// A stray shutdown_request envelope must never reach the leader's prompt —
// it's a control message addressed teammate-bound only. Dropping defensively
// here keeps protocol JSON out of the model's view if the protocol ever grows
// a teammate→leader shutdown path before the typed handler arrives.
func TestLeaderInboxPump_DropsShutdownRequest(t *testing.T) {
	reg := team.NewRegistry()
	if err := reg.CreateTeam("alpha", "", "sess-1"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	inj := newFakeInjector()
	cancel, done := startPump(t, reg, inj)
	defer func() {
		cancel()
		<-done
	}()

	mb := reg.Mailbox(team.TeamLeadName)
	if err := mb.Send(team.Message{From: "researcher", Text: cbteam.EncodeShutdownRequest("done")}); err != nil {
		t.Fatalf("Send shutdown: %v", err)
	}
	// Send a plain follow-up so we can observe that only the second arrives;
	// without this the test could pass trivially by the shutdown never landing.
	if err := mb.Send(team.Message{From: "researcher", Text: "back online"}); err != nil {
		t.Fatalf("Send follow-up: %v", err)
	}

	got := inj.waitForCount(t, 1, time.Second)
	if n := len(got); n != 1 {
		t.Fatalf("expected exactly 1 injected message (shutdown dropped), got %d", n)
	}
	if body := got[0].TextContent(); !strings.Contains(body, "back online") {
		t.Errorf("expected follow-up to survive, got %q", body)
	}
}

func TestLeaderInboxPump_WaitsForTeamCreation(t *testing.T) {
	reg := team.NewRegistry()
	inj := newFakeInjector()
	cancel, done := startPump(t, reg, inj)
	defer func() {
		cancel()
		<-done
	}()

	// No team yet — pump should be sleeping. Sleep a bit to confirm nothing
	// gets injected from the void.
	time.Sleep(30 * time.Millisecond)
	if n := len(inj.snapshot()); n != 0 {
		t.Fatalf("pump injected %d messages with no team active", n)
	}

	if err := reg.CreateTeam("alpha", "", "sess-1"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if err := reg.Mailbox(team.TeamLeadName).Send(team.Message{From: "x", Text: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	inj.waitForCount(t, 1, time.Second)
}

func TestLeaderInboxPump_SurvivesTeamDeletion(t *testing.T) {
	reg := team.NewRegistry()
	if err := reg.CreateTeam("alpha", "", "sess-1"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	inj := newFakeInjector()
	cancel, done := startPump(t, reg, inj)
	defer func() {
		cancel()
		<-done
	}()

	if err := reg.DeleteTeam(); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}

	// After deletion the pump should be back in backoff. Give it a tick to
	// notice — then verify it didn't exit by observing the goroutine is still
	// running (done channel still open).
	time.Sleep(20 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("pump exited unexpectedly when team was deleted")
	default:
	}
}

func TestLeaderInboxPump_ExitsOnContextCancel(t *testing.T) {
	reg := team.NewRegistry()
	inj := newFakeInjector()
	cancel, done := startPump(t, reg, inj)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pump did not exit within 1s of context cancel")
	}
}

func TestLeaderInboxPump_DefaultsWaitInterval(t *testing.T) {
	p := NewLeaderInboxPump(team.NewRegistry(), newFakeInjector(), 0)
	if p.waitInterval != defaultPumpWaitInterval {
		t.Errorf("zero waitInterval should pick default %v, got %v", defaultPumpWaitInterval, p.waitInterval)
	}
}
