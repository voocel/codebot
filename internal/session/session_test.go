package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/catalog"
	"github.com/voocel/litellm/litellmtest"

	"github.com/voocel/codebot/internal/storage"
)

const timeout = 5 * time.Second

// step answers one model call.
type step func(ctx context.Context) (litellmtest.Reply, error)

// fakeModel is a provider answering calls with its scripted steps, then
// with "ok".
type fakeModel struct {
	mu    sync.Mutex
	steps []step
	seen  [][]litellm.Message
}

func script(steps ...step) *fakeModel { return &fakeModel{steps: steps} }

func (m *fakeModel) Name() string { return "fake" }

func (m *fakeModel) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	reply, err := m.call(ctx, req)
	if err != nil {
		return nil, err
	}
	return litellmtest.New(reply).Chat(ctx, req)
}

func (m *fakeModel) Stream(ctx context.Context, req *litellm.Request) (litellm.Stream, error) {
	reply, err := m.call(ctx, req)
	if err != nil {
		return nil, err
	}
	return litellmtest.New(reply).Stream(ctx, req)
}

func (m *fakeModel) call(ctx context.Context, req *litellm.Request) (litellmtest.Reply, error) {
	m.mu.Lock()
	m.seen = append(m.seen, req.Messages)
	next := say("ok")
	if len(m.steps) > 0 {
		next, m.steps = m.steps[0], m.steps[1:]
	}
	m.mu.Unlock()
	return next(ctx)
}

// calls returns the messages each call received, after the system prompt,
// as describe renders them.
func (m *fakeModel) calls() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]string, len(m.seen))
	for i, msgs := range m.seen {
		for _, msg := range msgs {
			if msg.Role != litellm.RoleSystem {
				out[i] = append(out[i], describe(agentcore.Message{Role: msg.Role, Blocks: msg.Blocks}))
			}
		}
	}
	return out
}

func reply(text string) litellmtest.Reply {
	r := litellmtest.Text(text)
	r.Usage = litellm.Usage{InputTokens: 100, OutputTokens: 10}
	return r
}

func say(text string) step {
	return func(context.Context) (litellmtest.Reply, error) { return reply(text), nil }
}

// useTool calls the "wait" tool with the given id.
func useTool(id string) step {
	return func(context.Context) (litellmtest.Reply, error) {
		return litellmtest.Respond(litellm.ToolUseBlock{ID: id, Name: "wait", Arguments: `{}`}), nil
	}
}

// hang blocks until the call is canceled.
func hang(started chan<- struct{}) step {
	return func(ctx context.Context) (litellmtest.Reply, error) {
		close(started)
		<-ctx.Done()
		return litellmtest.Reply{}, ctx.Err()
	}
}

// describe renders a message compactly: "user:hi", "assistant:ok", "tool:t1".
func describe(m agentcore.Message) string {
	if m.Kind == agentcore.KindSummary {
		return "summary:" + agentcore.SummaryText(m)
	}
	if result, ok := m.ToolResult(); ok {
		return "tool:" + result.ToolUseID
	}
	switch {
	case m.Stop == agentcore.StopAborted:
		return "aborted"
	case len(m.ToolCalls()) > 0:
		return "call:" + m.ToolCalls()[0].ID
	}
	return string(m.Role) + ":" + m.Text()
}

func describeAll(msgs []agentcore.Message) string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = describe(m)
	}
	return strings.Join(out, " ")
}

// waitTool is a tool whose calls block until released.
type waitTool struct {
	started chan string
	release chan struct{}
}

func newWaitTool() *waitTool {
	return &waitTool{started: make(chan string, 16), release: make(chan struct{})}
}

func (w *waitTool) tool() agentcore.Tool {
	return agentcore.Tool{Name: "wait", Description: "waits", Schema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, _ json.RawMessage) (agentcore.Result, error) {
			w.started <- "wait"
			select {
			case <-w.release:
				return agentcore.TextResult("done"), nil
			case <-ctx.Done():
				return agentcore.Result{}, ctx.Err()
			}
		}}
}

// fakeCompactor keeps the last message after a summary.
type fakeCompactor struct {
	gate chan struct{} // when set, Compact waits for it
}

func (c fakeCompactor) Compact(ctx context.Context, msgs []agentcore.Message, _ func([]agentcore.Message) agentcore.Call) (*agentcore.Compaction, error) {
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if len(msgs) < 2 {
		return nil, nil
	}
	kept := msgs[len(msgs)-1:]
	out := append([]agentcore.Message{agentcore.SummaryMessage(fmt.Sprintf("%d earlier", len(msgs)-1))}, kept...)
	return &agentcore.Compaction{Messages: out, Replaced: len(msgs) - 1}, nil
}

// harness runs a session on a fresh log and records its events.
type harness struct {
	t     *testing.T
	s     *Session
	store *storage.Store

	mu     sync.Mutex
	cond   *sync.Cond
	events []Event
}

func spec(p litellm.Provider, tools ...agentcore.Tool) RunSpec {
	client, err := litellm.New(p)
	if err != nil {
		panic(err)
	}
	return RunSpec{
		Provider: "fake",
		Model:    "m1",
		Window:   100_000,
		Config: agentcore.Config{
			Model:     agentcore.Model{Client: client, Request: litellm.Request{Model: "m1"}, Pricing: &catalog.Pricing{InputCostPerToken: 1e-4}},
			System:    []litellm.Block{litellm.Text("system")},
			Tools:     tools,
			Compactor: fakeCompactor{},
		},
	}
}

func start(t *testing.T, sp RunSpec) *harness {
	t.Helper()
	store, err := storage.NewManager(t.TempDir()).Create("/work")
	if err != nil {
		t.Fatal(err)
	}
	return startOn(t, store, storage.State{}, sp)
}

func startOn(t *testing.T, store *storage.Store, state storage.State, sp RunSpec) *harness {
	t.Helper()
	s, err := Open(store, state, sp)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, s: s, store: store}
	h.cond = sync.NewCond(&h.mu)
	s.Subscribe(func(ev Event) {
		h.mu.Lock()
		h.events = append(h.events, ev)
		h.mu.Unlock()
		h.cond.Broadcast()
	})
	t.Cleanup(s.Close)
	return h
}

func (h *harness) post(src Source, text string) {
	h.s.Post(Input{Source: src, Msg: agentcore.UserText(text)})
}

// waitCount waits until n events match pred.
func (h *harness) waitCount(n int, pred func(Event) bool) {
	h.t.Helper()
	deadline := time.AfterFunc(timeout, h.cond.Broadcast)
	defer deadline.Stop()
	start := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for h.count(pred) < n {
		if time.Since(start) > timeout {
			h.t.Fatalf("timed out waiting for event %d", n)
		}
		h.cond.Wait()
	}
}

func (h *harness) count(pred func(Event) bool) int {
	n := 0
	for _, ev := range h.events {
		if pred(ev) {
			n++
		}
	}
	return n
}

func (h *harness) eventCount(pred func(Event) bool) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count(pred)
}

func (h *harness) waitIdle(n int) { h.t.Helper(); h.waitCount(n, isKind(Idle)) }

func isKind(k Kind) func(Event) bool { return func(ev Event) bool { return ev.Kind == k } }

// isAgent reports whether ev passes on an event of type E.
func isAgent[E agentcore.Event](ev Event) bool {
	_, ok := ev.Agent.(E)
	return ev.Kind == Agent && ok
}

// checkReplay asserts the invariant History() == Replay(log). The session
// must be quiescent: idle or closed.
func (h *harness) checkReplay() {
	h.t.Helper()
	state, err := storage.Replay(h.store.Path())
	if err != nil {
		h.t.Fatal(err)
	}
	if got, want := encode(h.t, h.s.History()), encode(h.t, state.Messages); got != want {
		raw, _ := os.ReadFile(h.store.Path())
		h.t.Fatalf("history differs from its log:\nmemory: %s\nlog:    %s\nfile:\n%s", got, want, raw)
	}
}

func encode(t *testing.T, msgs []agentcore.Message) string {
	t.Helper()
	var b strings.Builder
	for _, m := range msgs {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s\n", data)
	}
	return b.String()
}

func (h *harness) history() string { return describeAll(h.s.History()) }

func TestPromptRunsToIdle(t *testing.T) {
	t.Parallel()
	model := script(say("hello"))
	h := start(t, spec(model))

	h.post(User, "hi")
	h.waitIdle(1)

	if got := h.history(); got != "user:hi assistant:hello" {
		t.Fatalf("history = %s", got)
	}
	st := h.s.Status()
	if st.Running || st.LastRun == nil || st.LastRun.Reason != agentcore.EndDone {
		t.Fatalf("status = %+v", st)
	}
	if st.Usage.Input != 100 || st.Usage.Cost.Total != 0.01 || st.Context == 0 {
		t.Fatalf("usage = %+v, context = %d", st.Usage, st.Context)
	}
	h.checkReplay()
}

// RunStarted precedes the events of a run, and Idle follows them.
func TestRunEventsBetweenStartAndIdle(t *testing.T) {
	t.Parallel()
	h := start(t, spec(script()))
	h.post(User, "hi")
	h.waitIdle(1)

	h.mu.Lock()
	defer h.mu.Unlock()
	started := slices.IndexFunc(h.events, isKind(RunStarted))
	if first := slices.IndexFunc(h.events, isKind(Agent)); started < 0 || first < started {
		t.Fatalf("run started at %d, first agent event at %d", started, first)
	}
	end := slices.IndexFunc(h.events, isAgent[agentcore.RunEnd])
	idle := slices.IndexFunc(h.events, isKind(Idle))
	if end < 0 || idle < end {
		t.Fatalf("idle at %d, agent end at %d", idle, end)
	}
	for _, ev := range h.events[idle:] {
		if ev.Kind == Agent {
			t.Fatalf("agent event %T after idle", ev.Agent)
		}
	}
}

func TestUserInputSteersALiveRun(t *testing.T) {
	t.Parallel()
	tool := newWaitTool()
	model := script(useTool("t1"), say("steered"))
	h := start(t, spec(model, tool.tool()))

	h.post(User, "go")
	<-tool.started
	h.post(User, "also this")
	h.sync()
	close(tool.release)
	h.waitIdle(1)

	if got := h.history(); got != "user:go call:t1 tool:t1 user:also this assistant:steered" {
		t.Fatalf("history = %s", got)
	}
	if n := h.eventCount(isAgent[agentcore.RunEnd]); n != 1 {
		t.Fatalf("runs = %d, want 1", n)
	}
	h.checkReplay()
}

func TestBackgroundInputJoinsBeforeTheRunEnds(t *testing.T) {
	t.Parallel()
	tool := newWaitTool()
	model := script(useTool("t1"), say("after tool"), say("saw background"))
	h := start(t, spec(model, tool.tool()))

	h.post(User, "go")
	<-tool.started
	h.post(Background, "task finished")
	h.sync()
	close(tool.release)
	h.waitIdle(1)

	// Not inserted at the tool boundary, but before the run ends.
	if got := h.history(); got != "user:go call:t1 tool:t1 assistant:after tool user:task finished assistant:saw background" {
		t.Fatalf("history = %s", got)
	}
	if n := h.eventCount(isAgent[agentcore.RunEnd]); n != 1 {
		t.Fatalf("runs = %d, want 1", n)
	}
}

func TestBackgroundInputStartsARunWhenIdle(t *testing.T) {
	t.Parallel()
	h := start(t, spec(script()))
	h.post(Background, "task finished")
	h.waitIdle(1)
	if got := h.history(); got != "user:task finished assistant:ok" {
		t.Fatalf("history = %s", got)
	}
}

func TestCancelDropsQueuedUserInput(t *testing.T) {
	t.Parallel()
	tool := newWaitTool()
	model := script(useTool("t1"))
	h := start(t, spec(model, tool.tool()))

	h.post(User, "go")
	<-tool.started
	h.post(User, "typed while running")
	h.s.Cancel()
	h.waitIdle(1)

	// Cancelled between responses, the run records no aborted one.
	if got := h.history(); got != "user:go call:t1 tool:t1" {
		t.Fatalf("history = %s", got)
	}
	if st := h.s.Status(); st.LastRun == nil || st.LastRun.Reason != agentcore.EndAborted {
		t.Fatalf("last run = %+v", st.LastRun)
	}
	h.checkReplay()
}

func TestBackgroundInputWaitsAfterCancel(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	model := script(hang(started))
	h := start(t, spec(model))

	h.post(User, "go")
	<-started
	h.s.Cancel()
	h.waitIdle(1)

	h.post(Background, "task finished")
	h.sync()
	if n := h.eventCount(isAgent[agentcore.RunEnd]); n != 1 {
		t.Fatalf("background input started a run after cancel")
	}

	h.post(User, "next")
	h.waitIdle(2)
	if got := h.history(); got != "user:go user:task finished user:next assistant:ok" {
		t.Fatalf("history = %s", got)
	}
	h.checkReplay()
}

func TestUserInputAfterCancelStartsANewRun(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	model := script(hang(started))
	h := start(t, spec(model))

	h.post(User, "go")
	<-started
	h.s.Cancel()
	h.post(User, "instead")
	h.waitIdle(1)

	if got := h.history(); got != "user:go user:instead assistant:ok" {
		t.Fatalf("history = %s", got)
	}
	if n := h.eventCount(isKind(Idle)); n != 1 {
		t.Fatalf("idle events = %d, want 1 (the turn ends once)", n)
	}
}

func TestInputArrivingAsTheRunEndsStartsAnotherRun(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	followUpAsked := make(chan struct{})
	model := script(say("first"))
	sp := spec(model)
	// Hold the loop right after its last look at the queue.
	sp.Config.OnStop = func(context.Context, agentcore.StopInfo) ([]agentcore.Message, error) {
		close(followUpAsked)
		<-release
		return nil, nil
	}
	h := start(t, sp)

	h.post(User, "go")
	<-followUpAsked
	h.post(Background, "late")
	h.sync()
	sp.Config.OnStop = nil
	h.s.Configure(sp)
	close(release)
	h.waitIdle(1)

	if got := h.history(); got != "user:go assistant:first user:late assistant:ok" {
		t.Fatalf("history = %s", got)
	}
	if n, started := h.eventCount(isAgent[agentcore.RunEnd]), h.eventCount(isKind(RunStarted)); n != 2 || started != 2 {
		t.Fatalf("runs = %d, started %d, want 2", n, started)
	}
}

func TestCompactCancelsALiveRun(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	model := script(say("a1"), hang(started))
	h := start(t, spec(model))

	h.post(User, "u1")
	h.waitIdle(1)
	h.post(User, "u2")
	<-started
	if err := h.s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := h.history(); got != "summary:2 earlier user:u2" {
		t.Fatalf("history = %s", got)
	}
	h.waitIdle(2)
	h.checkReplay()
	if n := h.eventCount(isAgent[agentcore.CompactionEnd]); n != 1 {
		t.Fatalf("compaction end events = %d", n)
	}
}

func TestInputDuringCompactionRunsAfterIt(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	sp := spec(script(say("a1"), say("a2")))
	sp.Config.Compactor = fakeCompactor{gate: gate}
	h := start(t, sp)

	h.post(User, "u1")
	h.waitIdle(1)
	done := make(chan error)
	go func() { done <- h.s.Compact(context.Background()) }()
	h.waitCount(1, isAgent[agentcore.CompactionStart])
	h.post(User, "u2")
	h.post(Background, "bg")
	h.sync()
	if h.s.Status().Running {
		t.Fatal("a run started during compaction")
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.waitIdle(2)

	if got := h.history(); got != "summary:1 earlier assistant:a1 user:u2 user:bg assistant:a2" {
		t.Fatalf("history = %s", got)
	}
	h.checkReplay()
}

func TestCancelAbandonsAManualCompaction(t *testing.T) {
	t.Parallel()
	sp := spec(script(say("a1")))
	sp.Config.Compactor = fakeCompactor{gate: make(chan struct{})}
	h := start(t, sp)
	h.post(User, "u1")
	h.waitIdle(1)

	done := make(chan error)
	go func() { done <- h.s.Compact(context.Background()) }()
	h.waitCount(1, isAgent[agentcore.CompactionStart])
	h.s.Cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
	if got := h.history(); got != "user:u1 assistant:a1" {
		t.Fatalf("history = %s", got)
	}
}

func TestLoopCompactionIsRecorded(t *testing.T) {
	t.Parallel()
	sp := spec(script(say("a1"), say("a2")))
	sp.Config.CompactAt = 1 // every call compacts first
	h := start(t, sp)

	h.post(User, "u1")
	h.waitIdle(1)
	h.post(User, "u2")
	h.waitIdle(2)

	if n := h.eventCount(isAgent[agentcore.CompactionEnd]); n == 0 {
		t.Fatal("loop did not compact")
	}
	if got := h.history(); !strings.HasPrefix(got, "summary:") {
		t.Fatalf("history = %s", got)
	}
	h.checkReplay()
}

func TestConfigureAppliesToTheNextRun(t *testing.T) {
	t.Parallel()
	tool := newWaitTool()
	first := script(useTool("t1"), say("still first"))
	second := script(say("second"))
	h := start(t, spec(first, tool.tool()))

	h.post(User, "go")
	<-tool.started
	next := spec(second, tool.tool())
	next.Model, next.Effort = "m2", "high"
	h.s.Configure(next)
	h.waitStatus(func(st *Status) bool { return st.Model == "m2" })
	close(tool.release)
	h.waitIdle(1)
	h.post(User, "again")
	h.waitIdle(2)

	if got := h.history(); got != "user:go call:t1 tool:t1 assistant:still first user:again assistant:second" {
		t.Fatalf("history = %s", got)
	}
	state, err := storage.Replay(h.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if want := (storage.Model{Provider: "fake", Model: "m2", Effort: "high"}); state.Model != want {
		t.Fatalf("recorded model = %+v", state.Model)
	}
}

func TestQueryLeavesTheHistoryAlone(t *testing.T) {
	t.Parallel()
	model := script(say("a1"), say("  side answer "))
	h := start(t, spec(model))
	h.post(User, "u1")
	h.waitIdle(1)

	answer, err := h.s.Query(context.Background(), "what next?", nil)
	if err != nil || answer != "side answer" {
		t.Fatalf("answer = %q, %v", answer, err)
	}
	calls := model.calls()
	if got := strings.Join(calls[1], " "); got != "user:u1 assistant:a1 user:what next?" {
		t.Fatalf("query request = %s", got)
	}
	if got := h.history(); got != "user:u1 assistant:a1" {
		t.Fatalf("history = %s", got)
	}
}

func TestCloseCancelsTheRunAndDropsLaterInput(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	h := start(t, spec(script(hang(started))))

	h.post(User, "go")
	<-started
	h.s.Close()
	h.post(User, "too late")

	if got := h.history(); got != "user:go" {
		t.Fatalf("history = %s", got)
	}
	if err := h.s.Compact(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("compact after close: %v", err)
	}
	h.checkReplay()
}

func TestResumeContinuesTheLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := storage.NewManager(dir)
	store, err := m.Create("/work")
	if err != nil {
		t.Fatal(err)
	}
	h := startOn(t, store, storage.State{}, spec(script(say("a1"))))
	h.post(User, "u1")
	h.waitIdle(1)
	h.s.Close()

	store, state, err := m.Open(store.Header().SessionID)
	if err != nil {
		t.Fatal(err)
	}
	model := script(say("a2"))
	h = startOn(t, store, state, spec(model))
	if st := h.s.Status(); st.Usage.Input != 100 {
		t.Fatalf("resumed usage = %+v", st.Usage)
	}
	h.post(User, "u2")
	h.waitIdle(1)
	if got := strings.Join(model.calls()[0], " "); got != "user:u1 assistant:a1 user:u2" {
		t.Fatalf("resumed request = %s", got)
	}
	h.checkReplay()
}

// TestHistoryMatchesTheLog drives random interleavings of every operation
// and checks that memory and the log end up the same.
func TestHistoryMatchesTheLog(t *testing.T) {
	t.Parallel()
	for seed := range uint64(20) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, seed))
			model := &randomModel{rng: rand.New(rand.NewPCG(seed, 1))}
			sp := spec(model, quickTool())
			sp.Config.CompactAt = 400
			h := start(t, sp)

			for i := range 40 {
				switch rng.IntN(10) {
				case 0, 1, 2:
					h.post(User, fmt.Sprintf("u%d", i))
				case 3, 4:
					h.post(Background, fmt.Sprintf("b%d", i))
				case 5:
					h.s.Cancel()
				case 6:
					go h.s.Compact(context.Background())
				case 7:
					next := sp
					next.Model = fmt.Sprintf("m%d", i)
					h.s.Configure(next)
				case 8, 9:
					time.Sleep(time.Duration(rng.IntN(3)) * time.Millisecond)
				}
			}
			h.s.Close()
			h.checkReplay()
		})
	}
}

// randomModel answers with text or a tool call, at random.
type randomModel struct {
	mu  sync.Mutex
	rng *rand.Rand
	n   int
}

func (m *randomModel) next() litellmtest.Reply {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.n++
	if m.rng.IntN(2) == 0 {
		return reply(strings.Repeat("words ", m.rng.IntN(100)))
	}
	return litellmtest.Respond(litellm.ToolUseBlock{ID: fmt.Sprint("c", m.n), Name: "quick", Arguments: `{}`})
}

func (m *randomModel) Name() string { return "random" }

func (m *randomModel) Chat(ctx context.Context, req *litellm.Request) (*litellm.Response, error) {
	return litellmtest.New(m.next()).Chat(ctx, req)
}

func (m *randomModel) Stream(ctx context.Context, req *litellm.Request) (litellm.Stream, error) {
	return litellmtest.New(m.next()).Stream(ctx, req)
}

func quickTool() agentcore.Tool {
	return agentcore.Tool{Name: "quick", Description: "returns at once", Schema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, _ json.RawMessage) (agentcore.Result, error) {
			return agentcore.TextResult("quick result"), ctx.Err()
		}}
}

// sync waits until the session has applied every call made before it.
func (h *harness) sync() { h.s.do(func() {}) }

// waitStatus syncs, then waits until the status satisfies ok.
func (h *harness) waitStatus(ok func(*Status) bool) {
	h.t.Helper()
	h.sync()
	deadline := time.Now().Add(timeout)
	for st := h.s.Status(); !ok(&st); st = h.s.Status() {
		if time.Now().After(deadline) {
			h.t.Fatalf("status never matched: %+v", st)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWaitReturnsOnceSubscribersHaveTheIdle(t *testing.T) {
	t.Parallel()
	h := start(t, spec(script(say("hello"))))

	h.post(User, "hi")
	if err := h.s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := h.eventCount(isKind(Idle)); n != 1 {
		t.Fatalf("Wait returned with %d Idle events delivered", n)
	}
}

// A subscriber still behind on an earlier Idle must not end the wait for
// input posted after it: Wait covers the run the input starts.
func TestWaitCoversInputAfterAnEarlierIdle(t *testing.T) {
	t.Parallel()
	h := start(t, spec(script(say("first"), say("second"))))
	gate := make(chan struct{})
	var mu sync.Mutex
	var seen []string
	h.s.Subscribe(func(ev Event) {
		if ev.Kind == Idle {
			<-gate // hold the first Idle until the second input is posted
		}
		if e, ok := ev.Agent.(agentcore.MessageEnd); ok && ev.Kind == Agent && e.Message.Role == litellm.RoleAssistant {
			mu.Lock()
			seen = append(seen, e.Message.Text())
			mu.Unlock()
		}
	})

	h.post(Background, "one")
	h.waitIdle(1)
	h.post(User, "two")
	done := make(chan error, 1)
	go func() { done <- h.s.Wait(context.Background()) }()
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(seen, []string{"first", "second"}) {
		t.Fatalf("Wait returned before the second run's events were delivered: %q", seen)
	}
}

func TestWaitOnAnIdleSessionReturnsAtOnce(t *testing.T) {
	t.Parallel()
	h := start(t, spec(script()))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := h.s.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	h.s.Close()
	if err := h.s.Wait(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("Wait after Close = %v, want ErrClosed", err)
	}
}

// A run canceled before it reached the model still ends.
func TestCancelRightAfterPost(t *testing.T) {
	t.Parallel()
	h := start(t, spec(script(hang(make(chan struct{})))))

	h.post(User, "go")
	h.s.Cancel()
	h.waitIdle(1)

	if st := h.s.Status(); st.LastRun == nil || st.LastRun.Reason != agentcore.EndAborted {
		t.Fatalf("last run = %+v", st.LastRun)
	}
}
