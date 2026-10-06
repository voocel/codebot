package storage

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/catalog"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := create(dir, dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func appendRaw(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func texts(msgs []agentcore.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Text()
	}
	return out
}

// Replay applies every entry kind, and keeps thinking whole: a truncated one
// would break the signature and the prompt cache of a resumed session.
func TestReplayAppliesEveryEntryKind(t *testing.T) {
	t.Parallel()
	s := newStore(t)

	thinking := litellm.ReasoningBlock{Text: strings.Repeat("x", 10_000), State: &litellm.ProviderState{Provider: "anthropic", Data: json.RawMessage(`{"signature":"s"}`)}}
	answer := agentcore.Message{
		Role:   litellm.RoleAssistant,
		Blocks: []litellm.Block{thinking, litellm.Text("a1")},
		Usage:  &agentcore.Usage{Usage: litellm.Usage{InputTokens: 10, OutputTokens: 2}, Cost: &catalog.Cost{Total: 0.5}},
	}
	summary := agentcore.SummaryMessage("checkpoint")
	steps := []func() error{
		func() error { return s.AppendModel(Model{Provider: "p", Model: "m1"}) },
		func() error { return s.Append(agentcore.UserText("u1")) },
		func() error { return s.Append(answer) },
		func() error {
			return s.AppendCompaction(&agentcore.Compaction{Messages: []agentcore.Message{summary, answer}, Usage: &agentcore.Usage{Usage: litellm.Usage{InputTokens: 5}}})
		},
		func() error { return s.Append(agentcore.UserText("u2")) },
		func() error { return s.Append(answer) },
		func() error { return s.AppendModel(Model{Provider: "p", Model: "m2", Effort: "high"}) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}

	state, err := Replay(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(texts(state.Messages[1:]), ","), "a1,u2,a1"; got != want {
		t.Fatalf("messages = %s, want %s", got, want)
	}
	if got := state.Messages[0]; got.Kind != agentcore.KindSummary || agentcore.SummaryText(got) != "checkpoint" {
		t.Fatalf("summary did not round-trip: %#v", got)
	}
	if got, ok := state.Messages[3].Blocks[0].(litellm.ReasoningBlock); !ok || got.Text != thinking.Text || got.State == nil || string(got.State.Data) != `{"signature":"s"}` {
		t.Fatal("thinking must be stored verbatim so a resumed request matches the live one")
	}
	if state.Model != (Model{Provider: "p", Model: "m2", Effort: "high"}) {
		t.Fatalf("model = %+v", state.Model)
	}
	// Usage counts every recorded response, the replaced one included, and
	// the compaction.
	if state.Usage.InputTokens != 25 || state.Usage.Cost.Total != 1 {
		t.Fatalf("usage = %+v", state.Usage)
	}
}

func TestReplayReadsLinesLargerThanScannerLimit(t *testing.T) {
	t.Parallel()
	s := newStore(t)

	large := strings.Repeat("x", 2*1024*1024)
	for _, text := range []string{large, "after"} {
		if err := s.Append(agentcore.UserText(text)); err != nil {
			t.Fatal(err)
		}
	}
	state, err := Replay(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) != 2 || state.Messages[0].Text() != large || state.Messages[1].Text() != "after" {
		t.Fatalf("unexpected messages: %d", len(state.Messages))
	}
}

func TestOpenCutsCrashTornFinalLine(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if err := s.Append(agentcore.UserText("before crash")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	appendRaw(t, s.Path(), `{"kind":"message","data":{"mess`)

	resumed, state, err := open(s.Path())
	if err != nil {
		t.Fatalf("open after torn tail: %v", err)
	}
	if got := texts(state.Messages); len(got) != 1 || got[0] != "before crash" {
		t.Fatalf("recovered messages = %q", got)
	}
	if err := resumed.Append(agentcore.UserText("after recovery")); err != nil {
		t.Fatal(err)
	}
	resumed.Close()

	state, err = Replay(s.Path())
	if err != nil {
		t.Fatalf("replay after recovery append: %v", err)
	}
	if got := texts(state.Messages); len(got) != 2 || got[1] != "after recovery" {
		t.Fatalf("messages after recovery = %q", got)
	}
}

func TestOpenTerminatesValidFinalLine(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if err := s.Append(agentcore.UserText("complete without newline")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(s.Path(), info.Size()-1); err != nil {
		t.Fatal(err)
	}

	resumed, _, err := open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.Append(agentcore.UserText("next")); err != nil {
		t.Fatal(err)
	}
	resumed.Close()

	state, err := Replay(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(state.Messages))
	}
}

func TestOpenRejectsCorruption(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]string{
		"malformed line": "not-json\n",
		"unknown kind":   `{"kind":"llm_call","data":{}}` + "\n",
		"second header":  `{"kind":"header","data":{}}` + "\n",
		"empty message":  `{"kind":"message","data":{}}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			s.Close()
			appendRaw(t, s.Path(), line)
			if _, _, err := open(s.Path()); err == nil {
				t.Fatal("expected corruption in durable history to be reported")
			}
		})
	}
}

func TestOpenRejectsOtherVersions(t *testing.T) {
	t.Parallel()
	path := t.TempDir() + "/old.jsonl"
	if err := os.WriteFile(path, []byte(`{"kind":"header","id":"h0","data":{"version":3}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := open(path); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("err = %v, want version error", err)
	}
}

func TestManagerListsSessions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := NewManager(dir)

	older, err := m.Create("/work")
	if err != nil {
		t.Fatal(err)
	}
	reminder := agentcore.UserText("<system-reminder>x</system-reminder>")
	reminder.Kind = "reminder"
	for _, msg := range []agentcore.Message{reminder, agentcore.UserText("fix the bug"), agentcore.UserText("thanks")} {
		if err := older.Append(msg); err != nil {
			t.Fatal(err)
		}
	}
	older.Close()
	time.Sleep(10 * time.Millisecond)
	newer, err := m.Create("/work")
	if err != nil {
		t.Fatal(err)
	}
	newer.Close()
	if err := os.WriteFile(dir+"/stale.jsonl", []byte(`{"kind":"header","data":{"version":3}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != newer.Header().SessionID {
		t.Fatalf("list = %+v", list)
	}
	// The harness's reminder is not the conversation's.
	if got := list[1]; got.MessageCount != 2 || got.FirstMessage != "fix the bug" || got.Cwd != "/work" {
		t.Fatalf("info = %+v", got)
	}

	store, state, err := m.Open(older.Header().SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if len(state.Messages) != 3 {
		t.Fatalf("messages = %d", len(state.Messages))
	}
}
