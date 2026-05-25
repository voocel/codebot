package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
)

// scriptModel returns predetermined assistant messages in order — one per
// Generate / GenerateStream call. Tests use this to drive the teammate loop
// without touching real LLM infrastructure.
type scriptModel struct {
	responses []agentcore.Message
	idx       int64
}

func newScriptModel(texts ...string) *scriptModel {
	msgs := make([]agentcore.Message, len(texts))
	for i, t := range texts {
		msgs[i] = agentcore.Message{
			Role:       agentcore.RoleAssistant,
			Content:    []agentcore.ContentBlock{agentcore.TextBlock(t)},
			StopReason: agentcore.StopReasonStop,
		}
	}
	return &scriptModel{responses: msgs}
}

func (m *scriptModel) take() (agentcore.Message, error) {
	i := int(atomic.AddInt64(&m.idx, 1) - 1)
	if i >= len(m.responses) {
		return agentcore.Message{}, errors.New("scriptModel: no more responses")
	}
	return m.responses[i], nil
}

func (m *scriptModel) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	msg, err := m.take()
	if err != nil {
		return nil, err
	}
	return &agentcore.LLMResponse{Message: msg}, nil
}

func (m *scriptModel) GenerateStream(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	msg, err := m.take()
	if err != nil {
		return nil, err
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: msg, StopReason: msg.StopReason}
	close(ch)
	return ch, nil
}

func (m *scriptModel) SupportsTools() bool { return true }

// captureModel is a scriptModel variant that records every Generate /
// GenerateStream input. Tests use it to assert what the loop actually fed
// to the LLM (system prompt shape, cache_control metadata, message order).
type captureModel struct {
	*scriptModel
	captured [][]agentcore.Message
}

func newCaptureModel(texts ...string) *captureModel {
	return &captureModel{scriptModel: newScriptModel(texts...)}
}

func (m *captureModel) Generate(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.captured = append(m.captured, append([]agentcore.Message(nil), msgs...))
	return m.scriptModel.Generate(ctx, msgs, tools, opts...)
}

func (m *captureModel) GenerateStream(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.captured = append(m.captured, append([]agentcore.Message(nil), msgs...))
	return m.scriptModel.GenerateStream(ctx, msgs, tools, opts...)
}

// fakeNamedTool is a stand-in for tools we need by Name only (the executor
// passes tools through to the loop, but our stub model never invokes them).
type fakeNamedTool struct{ n string }

func (f *fakeNamedTool) Name() string                                       { return f.n }
func (f *fakeNamedTool) Label() string                                      { return f.n }
func (f *fakeNamedTool) Description() string                                { return "" }
func (f *fakeNamedTool) Schema() map[string]any                             { return map[string]any{"type": "object"} }
func (f *fakeNamedTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func TestMergeTeammateTools_AppendsAndDedupes(t *testing.T) {
	base := []agentcore.Tool{&fakeNamedTool{n: "read"}, &fakeNamedTool{n: "edit"}}
	extras := []agentcore.Tool{&fakeNamedTool{n: "send_message"}, &fakeNamedTool{n: "read"}}

	merged := mergeTeammateTools(base, extras)
	got := toolNames(merged)
	want := []string{"read", "edit", "send_message"}
	if len(got) != len(want) {
		t.Fatalf("merged = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("merged[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMergeTeammateTools_NilExtras(t *testing.T) {
	base := []agentcore.Tool{&fakeNamedTool{n: "read"}}
	merged := mergeTeammateTools(base, nil)
	// Returning the same slice (or one with the same contents) is fine; the
	// invariant is that no surprise mutations happen.
	if len(merged) != 1 || merged[0].Name() != "read" {
		t.Errorf("unexpected merge result: %v", toolNames(merged))
	}
}

func TestBuildTeammateExecutor_ProducesAssistantMessage(t *testing.T) {
	cfg := subagent.Config{Name: "researcher", SystemPrompt: "you are a researcher"}
	model := newScriptModel("first turn output")
	exec := buildTeammateExecutor(cfg, nil, model, nil)

	prompt := agentcore.UserMsg("go investigate")
	produced, err := exec(context.Background(), []agentcore.AgentMessage{prompt})
	if err != nil {
		t.Fatalf("executor returned error: %v", err)
	}
	if len(produced) != 1 {
		t.Fatalf("expected 1 produced message, got %d: %v", len(produced), produced)
	}
	if got := produced[0].TextContent(); got != "first turn output" {
		t.Errorf("produced text = %q, want %q", got, "first turn output")
	}
	// Crucially: the produced slice must NOT echo back the input prompt.
	for _, m := range produced {
		if m.TextContent() == "go investigate" {
			t.Error("executor leaked the input prompt back into produced messages")
		}
	}
}

func TestBuildTeammateExecutor_RejectsEmpty(t *testing.T) {
	exec := buildTeammateExecutor(subagent.Config{}, nil, newScriptModel(), nil)
	_, err := exec(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on empty msgs")
	}
}

// Verifies the teammate's system prompt reaches the LLM as a SystemBlock
// with cache_control=ephemeral metadata, so Anthropic's prompt cache covers
// it across the teammate's many turns. Without this, every mailbox-driven
// turn would re-pay the full system+tools input-token bill.
func TestBuildTeammateExecutor_SystemPromptCarriesCacheControl(t *testing.T) {
	cfg := subagent.Config{Name: "researcher", SystemPrompt: "you are a researcher"}
	model := newCaptureModel("done")
	exec := buildTeammateExecutor(cfg, nil, model, nil)

	if _, err := exec(context.Background(), []agentcore.AgentMessage{agentcore.UserMsg("go")}); err != nil {
		t.Fatalf("executor: %v", err)
	}
	if len(model.captured) == 0 {
		t.Fatal("model never invoked")
	}
	first := model.captured[0]
	if len(first) == 0 || first[0].Role != agentcore.RoleSystem {
		t.Fatalf("first LLM message is not system role: %+v", first)
	}
	if first[0].TextContent() != "you are a researcher" {
		t.Errorf("system text = %q, want %q", first[0].TextContent(), "you are a researcher")
	}
	got, _ := first[0].Metadata["cache_control"].(string)
	if got != "ephemeral" {
		t.Errorf("cache_control metadata = %q, want \"ephemeral\"", got)
	}
}

// Empty SystemPrompt must not produce a blank SystemBlock — some providers
// reject an empty system message. We assert that no system message is sent
// at all when cfg.SystemPrompt is empty.
func TestBuildTeammateExecutor_EmptySystemPromptSendsNoSystemBlock(t *testing.T) {
	cfg := subagent.Config{Name: "anon"} // SystemPrompt left blank
	model := newCaptureModel("done")
	exec := buildTeammateExecutor(cfg, nil, model, nil)

	if _, err := exec(context.Background(), []agentcore.AgentMessage{agentcore.UserMsg("hi")}); err != nil {
		t.Fatalf("executor: %v", err)
	}
	first := model.captured[0]
	for _, m := range first {
		if m.Role == agentcore.RoleSystem {
			t.Errorf("unexpected system message when SystemPrompt is empty: %+v", m)
		}
	}
}

// fakeCtxMgr records how often Project / Sync are called so the test can
// verify the same instance survives across turns. The methods are no-ops
// otherwise — AgentLoop tolerates a manager that returns empty projections.
type fakeCtxMgr struct {
	projectCalls int
	syncCalls    int
}

func (f *fakeCtxMgr) Project(_ context.Context, msgs []agentcore.AgentMessage) (agentcore.ContextProjection, error) {
	f.projectCalls++
	return agentcore.ContextProjection{Messages: msgs}, nil
}
func (f *fakeCtxMgr) Compact(_ context.Context, msgs []agentcore.AgentMessage, _ agentcore.CompactReason) (agentcore.ContextCommitResult, error) {
	return agentcore.ContextCommitResult{}, nil
}
func (f *fakeCtxMgr) RecoverOverflow(_ context.Context, _ []agentcore.AgentMessage, _ error) (agentcore.ContextRecoveryResult, error) {
	return agentcore.ContextRecoveryResult{}, nil
}
func (f *fakeCtxMgr) Sync(_ []agentcore.AgentMessage) { f.syncCalls++ }
func (f *fakeCtxMgr) Usage() *agentcore.ContextUsage  { return nil }
func (f *fakeCtxMgr) Snapshot() *agentcore.ContextSnapshot {
	return nil
}

// Teammates can live hundreds of turns. If the executor reinstantiated the
// context manager every turn, summary state would never accumulate and
// long-running teammates would hit context-overflow errors no matter how
// good the manager is. This test pins down the invariant.
func TestBuildTeammateExecutor_ReusesContextManagerAcrossTurns(t *testing.T) {
	mgr := &fakeCtxMgr{}
	factoryCalls := 0
	cfg := subagent.Config{
		Name: "researcher",
		ContextManagerFactory: func(_ agentcore.ChatModel) agentcore.ContextManager {
			factoryCalls++
			return mgr
		},
	}
	exec := buildTeammateExecutor(cfg, nil, newScriptModel("turn1", "turn2", "turn3"), nil)

	// Drive three turns. Each turn calls AgentLoop once → ContextManager.Project
	// is invoked at least once per turn. The factory must only fire once.
	for i := range 3 {
		if _, err := exec(context.Background(), []agentcore.AgentMessage{agentcore.UserMsg("turn")}); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}

	if factoryCalls != 1 {
		t.Errorf("ContextManagerFactory called %d times, want exactly 1 (manager must persist across turns)", factoryCalls)
	}
	if mgr.projectCalls < 3 {
		t.Errorf("expected Project to fire at least once per turn, got %d calls over 3 turns", mgr.projectCalls)
	}
}

func TestTeammateSpawner_HappyPath(t *testing.T) {
	reg := team.NewRegistry()
	rt := task.NewRuntime()
	if err := reg.CreateTeam("alpha", "", "leader-1"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	// Two assistant responses: one for the initial prompt, one for the
	// hand-off message we'll send next.
	cfg := subagent.Config{
		Name:         "researcher",
		Model:        newScriptModel("initial done", "second done"),
		SystemPrompt: "you are a researcher",
		Tools:        []agentcore.Tool{&fakeNamedTool{n: "read"}},
	}
	spawner := TeammateSpawner(reg, rt, []agentcore.Tool{&fakeNamedTool{n: "send_message"}}, nil)

	res, err := spawner(context.Background(), subagent.TeamSpawnRequest{
		Config:        cfg,
		Name:          "alice",
		TeamName:      "alpha",
		InitialPrompt: "start digging",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if res.TaskID == "" || res.AgentID != "alice@alpha" {
		t.Errorf("unexpected spawn result: %+v", res)
	}

	// Verify the teammate is registered with the right type + identity.
	entry := rt.Get(res.TaskID)
	if entry == nil {
		t.Fatal("teammate Entry missing from runtime")
	}
	if entry.Type != task.TypeTeammate {
		t.Errorf("Entry.Type = %q, want %q", entry.Type, task.TypeTeammate)
	}
	if entry.Identity == nil || entry.Identity.AgentName != "alice" {
		t.Errorf("Identity missing or wrong: %+v", entry.Identity)
	}

	// Allow the goroutine to run through the initial turn and park on its
	// mailbox. Then tear it down so the test exits cleanly.
	waitFor(t, time.Second, func() bool {
		e := rt.Get(res.TaskID)
		return e != nil && e.IsIdle
	})

	// Stop the team — DeleteTeam closes mailboxes, which makes the Run loop
	// exit so we don't leak a goroutine across tests.
	_ = reg.DeleteTeam()
	waitFor(t, time.Second, func() bool {
		e := rt.Get(res.TaskID)
		return e != nil && e.Status.IsTerminal()
	})
}

// End-to-end: a hub wired to the spawner must receive the teammate's
// AgentLoop events. We don't assert exact event order (that's agentcore's
// contract, not ours) — just that something flows through.
func TestTeammateSpawner_PublishesToHub(t *testing.T) {
	reg := team.NewRegistry()
	rt := task.NewRuntime()
	hub := NewTeammateEventHub()
	if err := reg.CreateTeam("alpha", "", "leader-1"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	pres, cancelPres := hub.SubscribePresence()
	defer cancelPres()

	cfg := subagent.Config{
		Name:         "researcher",
		Model:        newScriptModel("done"),
		SystemPrompt: "you are a researcher",
	}
	spawner := TeammateSpawner(reg, rt, nil, hub)

	res, err := spawner(context.Background(), subagent.TeamSpawnRequest{
		Config:        cfg,
		Name:          "alice",
		TeamName:      "alpha",
		InitialPrompt: "go",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// Presence: alice should announce Started on the first published event.
	select {
	case ev := <-pres:
		if !ev.Started || ev.AgentName != "alice" {
			t.Errorf("first presence = %+v, want Started alice", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no presence event within 1s")
	}

	// Events: subscribe AFTER spawn — we only need to see new events from
	// the next teammate turn. To force a second turn, send the teammate a
	// message and wait for it to go idle again. The hub is the source of
	// truth, not task.Entry.
	_, ch, cancel := hub.Subscribe("alice")
	defer cancel()

	// Trigger a second turn by sending a peer message.
	mb := reg.Mailbox("alice")
	if mb == nil {
		t.Fatal("mailbox missing for alice")
	}
	if err := mb.Send(team.Message{From: team.TeamLeadName, Text: "ping"}); err != nil {
		t.Fatalf("mailbox send: %v", err)
	}

	// Drain a few events; we just need to see SOMETHING from agentcore.
	got := drainEvents(t, ch, 1, 2*time.Second)
	if len(got) == 0 {
		t.Error("no events delivered through hub within 2s")
	}

	_ = reg.DeleteTeam()
	waitFor(t, time.Second, func() bool {
		e := rt.Get(res.TaskID)
		return e != nil && e.Status.IsTerminal()
	})
}

func TestTeammateSpawner_RejectsWhenNoTeam(t *testing.T) {
	reg := team.NewRegistry()
	rt := task.NewRuntime()
	spawner := TeammateSpawner(reg, rt, nil, nil)

	_, err := spawner(context.Background(), subagent.TeamSpawnRequest{
		Config:        subagent.Config{Name: "researcher", Model: newScriptModel()},
		Name:          "alice",
		TeamName:      "alpha",
		InitialPrompt: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "no active team") {
		t.Errorf("expected no-team error, got %v", err)
	}
}

func TestTeammateSpawner_RejectsWrongTeamName(t *testing.T) {
	reg := team.NewRegistry()
	rt := task.NewRuntime()
	if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	spawner := TeammateSpawner(reg, rt, nil, nil)

	_, err := spawner(context.Background(), subagent.TeamSpawnRequest{
		Config:        subagent.Config{Name: "researcher", Model: newScriptModel()},
		Name:          "alice",
		TeamName:      "beta", // mismatched
		InitialPrompt: "x",
	})
	if err == nil || !strings.Contains(err.Error(), `does not match`) {
		t.Errorf("expected mismatch error, got %v", err)
	}
}

func TestTeammateSpawner_RejectsMissingModel(t *testing.T) {
	reg := team.NewRegistry()
	rt := task.NewRuntime()
	if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	spawner := TeammateSpawner(reg, rt, nil, nil)

	_, err := spawner(context.Background(), subagent.TeamSpawnRequest{
		Config:        subagent.Config{Name: "researcher"}, // no Model
		Name:          "alice",
		TeamName:      "alpha",
		InitialPrompt: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "no model configured") {
		t.Errorf("expected missing-model error, got %v", err)
	}
}

func TestTeammateSpawner_DepthGuard(t *testing.T) {
	reg := team.NewRegistry()
	rt := task.NewRuntime()
	if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	spawner := TeammateSpawner(reg, rt, nil, nil)

	// Caller already sits at MaxAgentDepth → spawn would push past it.
	ctx := task.WithDepth(context.Background(), task.MaxAgentDepth)
	_, err := spawner(ctx, subagent.TeamSpawnRequest{
		Config:        subagent.Config{Name: "researcher", Model: newScriptModel()},
		Name:          "alice",
		TeamName:      "alpha",
		InitialPrompt: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds max") {
		t.Errorf("expected depth error, got %v", err)
	}
}

func TestUniqueAgentName(t *testing.T) {
	tests := []struct {
		name       string
		registered []string // pre-registered teammate names (besides leader)
		base       string
		want       string
	}{
		{
			name:       "no conflict returns base",
			registered: nil,
			base:       "researcher",
			want:       "researcher",
		},
		{
			name:       "one conflict gets -2",
			registered: []string{"researcher"},
			base:       "researcher",
			want:       "researcher-2",
		},
		{
			name:       "consecutive conflicts skip to first free slot",
			registered: []string{"tester", "tester-2", "tester-3"},
			base:       "tester",
			want:       "tester-4",
		},
		{
			name:       "case-insensitive: existing Tester blocks tester",
			registered: []string{"Tester"},
			base:       "tester",
			want:       "tester-2",
		},
		{
			name:       "empty base returned as-is",
			registered: nil,
			base:       "",
			want:       "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := team.NewRegistry()
			if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
				t.Fatalf("CreateTeam: %v", err)
			}
			for i, n := range tt.registered {
				if err := reg.RegisterAgent(n, fmt.Sprintf("tm-%d", i)); err != nil {
					t.Fatalf("RegisterAgent %q: %v", n, err)
				}
			}
			if got := uniqueAgentName(reg, tt.base); got != tt.want {
				t.Errorf("uniqueAgentName(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}

func TestUniqueAgentName_NilRegistry(t *testing.T) {
	if got := uniqueAgentName(nil, "x"); got != "x" {
		t.Errorf("uniqueAgentName(nil, %q) = %q, want %q", "x", got, "x")
	}
}

// End-to-end: spawning the same logical name twice must auto-suffix instead
// of bubbling ErrAgentExists up to the model, matching cc's UX.
func TestTeammateSpawner_AutoSuffixesDuplicateName(t *testing.T) {
	reg := team.NewRegistry()
	rt := task.NewRuntime()
	if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	cfg := subagent.Config{
		Name:         "researcher",
		Model:        newScriptModel("first", "second", "third", "fourth"),
		SystemPrompt: "you are a researcher",
	}
	spawner := TeammateSpawner(reg, rt, nil, nil)

	first, err := spawner(context.Background(), subagent.TeamSpawnRequest{
		Config:        cfg,
		Name:          "researcher",
		InitialPrompt: "go",
	})
	if err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	if first.AgentID != "researcher@alpha" {
		t.Errorf("first AgentID = %q, want researcher@alpha", first.AgentID)
	}

	second, err := spawner(context.Background(), subagent.TeamSpawnRequest{
		Config:        cfg,
		Name:          "researcher",
		InitialPrompt: "go again",
	})
	if err != nil {
		t.Fatalf("second spawn: %v", err)
	}
	if second.AgentID != "researcher-2@alpha" {
		t.Errorf("second AgentID = %q, want researcher-2@alpha", second.AgentID)
	}

	// Tear down so the test exits cleanly.
	_ = reg.DeleteTeam()
	waitFor(t, time.Second, func() bool {
		e1 := rt.Get(first.TaskID)
		e2 := rt.Get(second.TaskID)
		return e1 != nil && e2 != nil && e1.Status.IsTerminal() && e2.Status.IsTerminal()
	})
}

// --- helpers -----------------------------------------------------------------

func toolNames(in []agentcore.Tool) []string {
	out := make([]string, len(in))
	for i, t := range in {
		out[i] = t.Name()
	}
	return out
}

