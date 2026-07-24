package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/agentcore/task"
	"github.com/voocel/agentcore/team"
	agenttools "github.com/voocel/agentcore/tools"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/worktree"
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

func (f *fakeNamedTool) Name() string           { return f.n }
func (f *fakeNamedTool) Label() string          { return f.n }
func (f *fakeNamedTool) Description() string    { return "" }
func (f *fakeNamedTool) Schema() map[string]any { return map[string]any{"type": "object"} }
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
	exec := buildTeammateExecutor(cfg, nil, model, nil, nil, nil, nil, "")

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
	exec := buildTeammateExecutor(subagent.Config{}, nil, newScriptModel(), nil, nil, nil, nil, "")
	_, err := exec(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on empty msgs")
	}
}

// Replace mode is the legacy single-block path: SystemPrompt is the only
// system content the teammate gets (no universal base, no addendum). Used
// by .agents/*.md definitions that already include everything they need.
// Asserts the single block carries cache_control=ephemeral so Anthropic's
// prompt cache covers it across the teammate's many turns.
func TestBuildTeammateExecutor_ReplaceMode_SingleBlockWithCacheControl(t *testing.T) {
	cfg := subagent.Config{
		Name:             "researcher",
		SystemPrompt:     "you are a researcher",
		SystemPromptMode: config.SystemPromptModeReplace,
	}
	model := newCaptureModel("done")
	exec := buildTeammateExecutor(cfg, nil, model, nil, nil, nil, nil, "")

	if _, err := exec(context.Background(), []agentcore.AgentMessage{agentcore.UserMsg("go")}); err != nil {
		t.Fatalf("executor: %v", err)
	}
	first := model.captured[0]
	var systemMsgs []agentcore.Message
	for _, m := range first {
		if m.Role == agentcore.RoleSystem {
			systemMsgs = append(systemMsgs, m)
		}
	}
	if len(systemMsgs) != 1 {
		t.Fatalf("Replace mode must produce exactly 1 system block, got %d", len(systemMsgs))
	}
	if systemMsgs[0].TextContent() != "you are a researcher" {
		t.Errorf("system text = %q, want %q", systemMsgs[0].TextContent(), "you are a researcher")
	}
	got, _ := systemMsgs[0].Metadata["cache_control"].(string)
	if got != "ephemeral" {
		t.Errorf("cache_control metadata = %q, want \"ephemeral\"", got)
	}
}

// Replace mode + empty SystemPrompt → no system block at all (some providers
// reject empty system messages, and a blank block carrying cache_control
// would still consume a marker for zero value).
func TestBuildTeammateExecutor_ReplaceMode_EmptyPromptSendsNoSystemBlock(t *testing.T) {
	cfg := subagent.Config{
		Name:             "anon",
		SystemPromptMode: config.SystemPromptModeReplace,
	}
	model := newCaptureModel("done")
	exec := buildTeammateExecutor(cfg, nil, model, nil, nil, nil, nil, "")

	if _, err := exec(context.Background(), []agentcore.AgentMessage{agentcore.UserMsg("hi")}); err != nil {
		t.Fatalf("executor: %v", err)
	}
	first := model.captured[0]
	for _, m := range first {
		if m.Role == agentcore.RoleSystem {
			t.Errorf("unexpected system message when SystemPrompt is empty in Replace mode: %+v", m)
		}
	}
}

// Default mode (the zero-value default) produces two system blocks:
// baseBlocks[0] verbatim, then a teammate role block wrapping
// cfg.SystemPrompt under "# Custom Agent Instructions" (H1). Both carry
// cache_control=ephemeral.
func TestBuildTeammateExecutor_DefaultMode_PrependsBaseBlocks(t *testing.T) {
	baseText := "## Environment\n- universal base content\n"
	cfg := subagent.Config{Name: "researcher", SystemPrompt: "you are a researcher"}
	// Zero-value SystemPromptMode → falls back to default.
	model := newCaptureModel("done")
	baseBlocks := []agentcore.SystemBlock{{Text: baseText, CacheControl: "ephemeral"}}
	exec := buildTeammateExecutor(cfg, nil, model, nil, nil, baseBlocks, nil, "")

	if _, err := exec(context.Background(), []agentcore.AgentMessage{agentcore.UserMsg("go")}); err != nil {
		t.Fatalf("executor: %v", err)
	}
	first := model.captured[0]
	var systemMsgs []agentcore.Message
	for _, m := range first {
		if m.Role == agentcore.RoleSystem {
			systemMsgs = append(systemMsgs, m)
		}
	}
	if len(systemMsgs) != 2 {
		t.Fatalf("Default mode must produce exactly 2 system blocks, got %d", len(systemMsgs))
	}
	if systemMsgs[0].TextContent() != baseText {
		t.Errorf("first block must be the base verbatim; got %q", systemMsgs[0].TextContent())
	}
	roleText := systemMsgs[1].TextContent()
	for _, marker := range []string{"team lead", "Mailbox", "\n# Custom Agent Instructions\n", "you are a researcher"} {
		if !strings.Contains(roleText, marker) {
			t.Errorf("role block missing %q; got %q", marker, roleText)
		}
	}
	// Header must be H1, not H2.
	if strings.Contains(roleText, "## Custom Agent Instructions") {
		t.Error("custom-instructions header must be H1, not H2")
	}
	for i, m := range systemMsgs {
		cc, _ := m.Metadata["cache_control"].(string)
		if cc != "ephemeral" {
			t.Errorf("system block %d cache_control = %q, want ephemeral", i, cc)
		}
	}
}

// Default mode + a dynamic block snapshot produces 3 system blocks:
// [base ephemeral][role ephemeral][dynamic no-cache]. The dynamic block
// MUST NOT carry cache_control — it sits after the cached prefix as the
// uncached tail (leader does the same in session_prompt.go).
func TestBuildTeammateExecutor_DefaultMode_AppendsDynamicBlock(t *testing.T) {
	baseText := "BASE"
	dynamicText := "## MCP Tools\n- **mcp__docs__search**: Search docs\n"
	cfg := subagent.Config{Name: "researcher", SystemPrompt: "you are a researcher"}
	model := newCaptureModel("done")
	baseBlocks := []agentcore.SystemBlock{{Text: baseText, CacheControl: "ephemeral"}}
	dynamic := &agentcore.SystemBlock{Text: dynamicText}
	exec := buildTeammateExecutor(cfg, nil, model, nil, nil, baseBlocks, dynamic, "")

	if _, err := exec(context.Background(), []agentcore.AgentMessage{agentcore.UserMsg("go")}); err != nil {
		t.Fatalf("executor: %v", err)
	}
	first := model.captured[0]
	var systemMsgs []agentcore.Message
	for _, m := range first {
		if m.Role == agentcore.RoleSystem {
			systemMsgs = append(systemMsgs, m)
		}
	}
	if len(systemMsgs) != 3 {
		t.Fatalf("Default+dynamic must produce 3 system blocks, got %d", len(systemMsgs))
	}
	if systemMsgs[2].TextContent() != dynamicText {
		t.Errorf("third block must be dynamic verbatim; got %q", systemMsgs[2].TextContent())
	}
	if cc, ok := systemMsgs[2].Metadata["cache_control"].(string); ok && cc != "" {
		t.Errorf("dynamic block must NOT carry cache_control; got %q", cc)
	}
	// And the first two MUST still be cached.
	for i := range 2 {
		cc, _ := systemMsgs[i].Metadata["cache_control"].(string)
		if cc != "ephemeral" {
			t.Errorf("system block %d cache_control = %q, want ephemeral", i, cc)
		}
	}
}

// An empty / nil dynamic block must NOT add a third entry — we don't want
// to send a blank block.
func TestBuildTeammateExecutor_DefaultMode_NilDynamicSkipsThirdBlock(t *testing.T) {
	cfg := subagent.Config{Name: "researcher", SystemPrompt: "you are a researcher"}
	model := newCaptureModel("done")
	baseBlocks := []agentcore.SystemBlock{{Text: "BASE", CacheControl: "ephemeral"}}

	// nil dynamic
	exec := buildTeammateExecutor(cfg, nil, model, nil, nil, baseBlocks, nil, "")
	if _, err := exec(context.Background(), []agentcore.AgentMessage{agentcore.UserMsg("go")}); err != nil {
		t.Fatalf("executor: %v", err)
	}
	systemBlocks := countSystemMessages(model.captured[0])
	if systemBlocks != 2 {
		t.Errorf("nil dynamic should yield 2 system blocks, got %d", systemBlocks)
	}

	// empty-Text dynamic
	model2 := newCaptureModel("done")
	emptyDyn := &agentcore.SystemBlock{Text: ""}
	exec2 := buildTeammateExecutor(cfg, nil, model2, nil, nil, baseBlocks, emptyDyn, "")
	if _, err := exec2(context.Background(), []agentcore.AgentMessage{agentcore.UserMsg("go")}); err != nil {
		t.Fatalf("executor: %v", err)
	}
	if got := countSystemMessages(model2.captured[0]); got != 2 {
		t.Errorf("empty-text dynamic should yield 2 system blocks, got %d", got)
	}
}

func countSystemMessages(msgs []agentcore.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == agentcore.RoleSystem {
			n++
		}
	}
	return n
}

// Append mode = Default + cfg.SystemPrompt at the end of the role block.
// Crucially the cache-controlled portion stays at 2 blocks (we do NOT open
// a third cache_control marker — Anthropic's cap is tight). A dynamic block
// can still tail on uncached, just like Default mode.
func TestBuildTeammateExecutor_AppendMode_NoExtraCacheBlock(t *testing.T) {
	cfg := subagent.Config{
		Name:             "researcher",
		SystemPrompt:     "EXTRA APPEND CONTENT",
		SystemPromptMode: config.SystemPromptModeAppend,
	}
	model := newCaptureModel("done")
	baseBlocks := []agentcore.SystemBlock{{Text: "BASE", CacheControl: "ephemeral"}}
	exec := buildTeammateExecutor(cfg, nil, model, nil, nil, baseBlocks, nil, "")

	if _, err := exec(context.Background(), []agentcore.AgentMessage{agentcore.UserMsg("go")}); err != nil {
		t.Fatalf("executor: %v", err)
	}
	first := model.captured[0]
	var systemMsgs []agentcore.Message
	for _, m := range first {
		if m.Role == agentcore.RoleSystem {
			systemMsgs = append(systemMsgs, m)
		}
	}
	if len(systemMsgs) != 2 {
		t.Fatalf("Append mode must produce exactly 2 system blocks, got %d", len(systemMsgs))
	}
	roleText := systemMsgs[1].TextContent()
	if !strings.Contains(roleText, "EXTRA APPEND CONTENT") {
		t.Errorf("role block must contain appended content; got %q", roleText)
	}
	if strings.Contains(roleText, "## Custom Agent Instructions") {
		t.Error("Append mode must NOT wrap content under '## Custom Agent Instructions' (that header is Default-mode only)")
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
	exec := buildTeammateExecutor(cfg, nil, newScriptModel("turn1", "turn2", "turn3"), nil, nil, nil, nil, "")

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
	spawner := TeammateSpawner(reg, rt, []agentcore.Tool{&fakeNamedTool{n: "send_message"}}, nil, nil, nil, team.ProtocolHooks{}, nil, nil, nil)

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
	spawner := TeammateSpawner(reg, rt, nil, hub, nil, nil, team.ProtocolHooks{}, nil, nil, nil)

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
	spawner := TeammateSpawner(reg, rt, nil, nil, nil, nil, team.ProtocolHooks{}, nil, nil, nil)

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
	spawner := TeammateSpawner(reg, rt, nil, nil, nil, nil, team.ProtocolHooks{}, nil, nil, nil)

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
	spawner := TeammateSpawner(reg, rt, nil, nil, nil, nil, team.ProtocolHooks{}, nil, nil, nil)

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
	spawner := TeammateSpawner(reg, rt, nil, nil, nil, nil, team.ProtocolHooks{}, nil, nil, nil)

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

// End-to-end: spawning the same logical name twice must auto-suffix
// instead of bubbling ErrAgentExists up to the model.
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
	spawner := TeammateSpawner(reg, rt, nil, nil, nil, nil, team.ProtocolHooks{}, nil, nil, nil)

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

// TestTeammateCwd verifies the working directory a spawned teammate runs in:
// an isolated teammate always gets its own worktree (even over a leader cwd on
// ctx); a shared teammate inherits the leader cwd carried on the spawn ctx, and
// falls back to "" (the tools' constructed WorkDir) when none is set.
func TestTeammateCwd(t *testing.T) {
	wt := &teammateWorktree{dir: "/tmp/wt-coder"}
	leaderCtx := agenttools.WithCwd(context.Background(), "/leader/cwd")

	if got := teammateCwd(wt, context.Background()); got != "/tmp/wt-coder" {
		t.Errorf("isolated (no leader cwd) = %q, want /tmp/wt-coder", got)
	}
	if got := teammateCwd(wt, leaderCtx); got != "/tmp/wt-coder" {
		t.Errorf("isolated must win over leader cwd, got %q", got)
	}
	if got := teammateCwd(nil, leaderCtx); got != "/leader/cwd" {
		t.Errorf("shared should inherit leader cwd, got %q", got)
	}
	if got := teammateCwd(nil, context.Background()); got != "" {
		t.Errorf("shared with no leader cwd = %q, want empty (fall back to WorkDir)", got)
	}
}

// TestTeammateSpawner_WorktreeIsolation verifies opt-in worktree sandboxing at
// the spawner level: a teammate whose agent type maps to "worktree" gets a
// private checkout created under .codebot/worktrees; a type that did not opt in
// gets no checkout (it shares the leader cwd via the spawn ctx). The teammate
// stays alive (no InitialPrompt) until DeleteTeam, so the checkout is observable
// before cleanup runs.
func TestTeammateSpawner_WorktreeIsolation(t *testing.T) {
	repo := initGitRepo(t)
	reg := team.NewRegistry()
	rt := task.NewRuntime()
	if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	iso := &TeammateIsolation{
		RepoRoot: repo,
		Of:       map[string]string{"coder": "worktree"},
	}
	spawner := TeammateSpawner(reg, rt, nil, nil, nil, nil, team.ProtocolHooks{}, nil, nil, iso)

	// Opted-in type: a private checkout is created under .codebot/worktrees.
	coder, err := spawner(context.Background(), subagent.TeamSpawnRequest{
		Config: subagent.Config{Name: "coder", Model: newScriptModel("done"), Tools: []agentcore.Tool{&fakeNamedTool{n: "read"}}},
		Name:   "coder",
	})
	if err != nil {
		t.Fatalf("spawn coder: %v", err)
	}
	infos, _ := worktree.List(repo)
	if len(infos) != 1 {
		t.Fatalf("opted-in teammate should create 1 worktree, got %d", len(infos))
	}
	if want := filepath.Join(".codebot", "worktrees", "wt-coder"); !strings.Contains(infos[0].Path, want) {
		t.Errorf("worktree path = %q, want it to contain %q", infos[0].Path, want)
	}

	// Non-opted-in type: shared cwd, no new checkout.
	plain, err := spawner(context.Background(), subagent.TeamSpawnRequest{
		Config: subagent.Config{Name: "plain", Model: newScriptModel("done")},
		Name:   "plain",
	})
	if err != nil {
		t.Fatalf("spawn plain: %v", err)
	}
	if infos, _ := worktree.List(repo); len(infos) != 1 {
		t.Errorf("shared teammate must not create a worktree, still %d total", len(infos))
	}

	_ = reg.DeleteTeam()
	waitFor(t, time.Second, func() bool {
		e1, e2 := rt.Get(coder.TaskID), rt.Get(plain.TaskID)
		return e1 != nil && e2 != nil && e1.Status.IsTerminal() && e2.Status.IsTerminal()
	})
}

// TestTeammateSpawner_IsolationFailsClosed verifies that a teammate which
// declares worktree isolation in a non-git directory fails the spawn rather than
// silently running in the shared cwd (where it could clobber peers — the very
// thing isolation exists to prevent).
func TestTeammateSpawner_IsolationFailsClosed(t *testing.T) {
	nonGit := t.TempDir() // a fresh temp dir is not a git repository
	reg := team.NewRegistry()
	rt := task.NewRuntime()
	if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	iso := &TeammateIsolation{
		RepoRoot: nonGit,
		Of:       map[string]string{"coder": "worktree"},
	}
	spawner := TeammateSpawner(reg, rt, nil, nil, nil, nil, team.ProtocolHooks{}, nil, nil, iso)

	_, err := spawner(context.Background(), subagent.TeamSpawnRequest{
		Config: subagent.Config{Name: "coder", Model: newScriptModel("done")},
		Name:   "coder",
	})
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("expected fail-closed git error, got %v", err)
	}
	if _, live := reg.TaskID("coder"); live {
		t.Error("no teammate should be registered when isolation fails closed")
	}
}

// TestTeammateWorktreeCleanup verifies the exit policy mirrors the leader-side
// /worktree exit: a clean sandbox is removed; a dirty one is preserved and the
// leader is notified where the unreviewed work lives.
func TestTeammateWorktreeCleanup(t *testing.T) {
	repo := initGitRepo(t)

	// Clean sandbox → removed (worktree + branch gone).
	clean, err := newTeammateWorktree(repo, "alice")
	if err != nil {
		t.Fatalf("newTeammateWorktree(alice): %v", err)
	}
	clean.cleanup(nil, "alice")
	if infos, _ := worktree.List(repo); len(infos) != 0 {
		t.Errorf("clean sandbox not removed, still listed: %+v", infos)
	}

	// Dirty sandbox → kept, leader notified.
	reg := team.NewRegistry()
	if err := reg.CreateTeam("alpha", "", "leader"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	dirty, err := newTeammateWorktree(repo, "bob")
	if err != nil {
		t.Fatalf("newTeammateWorktree(bob): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirty.dir, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty.cleanup(reg, "bob")
	if infos, _ := worktree.List(repo); len(infos) != 1 {
		t.Errorf("dirty sandbox should be preserved, got %d worktrees", len(infos))
	}
	if mb := reg.Mailbox(team.TeamLeadName); mb == nil || mb.Len() != 1 {
		got := -1
		if mb != nil {
			got = mb.Len()
		}
		t.Errorf("leader inbox = %d messages, want 1 (dirty-exit notification)", got)
	}
}

// TestTeammateBaseBlocks verifies an isolated teammate's system base is rebuilt
// for its worktree cwd (so the prompt's working directory matches its tools and
// can't invite absolute-path escapes), while shared teammates and SystemOverride
// sessions pass the leader's base through unchanged.
func TestTeammateBaseBlocks(t *testing.T) {
	mainCwd := "/main/repo-xyz"
	shared := []agentcore.SystemBlock{{Text: config.BuildUniversalBase(mainCwd), CacheControl: "ephemeral"}}

	// Shared teammate (wt == nil): leader's base verbatim.
	if got := teammateBaseBlocks(shared, nil); len(got) != 1 || got[0].Text != shared[0].Text {
		t.Errorf("shared teammate should inherit leader base verbatim, got %+v", got)
	}

	// Isolated teammate: base rebuilt for the worktree cwd, main cwd gone.
	wtCwd := "/tmp/sandbox-abc/.codebot/worktrees/wt-coder"
	wt := &teammateWorktree{dir: wtCwd}
	got := teammateBaseBlocks(shared, wt)
	if len(got) != 1 {
		t.Fatalf("isolated base = %d blocks, want 1", len(got))
	}
	if !strings.Contains(got[0].Text, wtCwd) {
		t.Errorf("isolated base must state the worktree cwd %q, got:\n%s", wtCwd, got[0].Text)
	}
	if strings.Contains(got[0].Text, mainCwd) {
		t.Errorf("isolated base must NOT leak the main cwd %q (sandbox-escape risk)", mainCwd)
	}
	if got[0].CacheControl != "ephemeral" {
		t.Errorf("isolated base CacheControl = %q, want ephemeral", got[0].CacheControl)
	}

	// SystemOverride session (empty shared base): passed through even when isolated.
	if got := teammateBaseBlocks(nil, wt); got != nil {
		t.Errorf("empty base must pass through untouched, got %+v", got)
	}
}

// --- helpers -----------------------------------------------------------------

// initGitRepo creates a throwaway git repository with one commit, for tests
// that exercise worktree isolation. The path is symlink-resolved to match
// `git worktree list` output (macOS /var -> /private/var).
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t.t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

func toolNames(in []agentcore.Tool) []string {
	out := make([]string, len(in))
	for i, t := range in {
		out[i] = t.Name()
	}
	return out
}
