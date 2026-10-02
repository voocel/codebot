package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/litellm"
)

// eventModel is a ready test model.
func eventModel() *Model {
	m := testModel("test-model")
	m.Ready = true
	m.Width = 80
	return m
}

// handle feeds events to m in order.
func handle(t *testing.T, m *Model, events ...agentcore.Event) *Model {
	t.Helper()
	for _, ev := range events {
		next, _ := m.HandleAgentEvent(ev)
		m = mustModel(t, next)
	}
	return m
}

// lastPrinted is the text of the latest block printed to scrollback.
func lastPrinted(m *Model) string {
	if len(m.Scrollback) == 0 {
		return ""
	}
	return ansi.Strip(m.Scrollback[len(m.Scrollback)-1])
}

func call(id, name, args string) agentcore.ToolCall {
	return agentcore.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
}

func TestRunLifecycle(t *testing.T) {
	m := eventModel()
	next, _ := m.Update(RunStartedMsg{})
	m = mustModel(t, next)
	if !m.Running || m.RunStats.StartedAt.IsZero() {
		t.Fatalf("Running = %v, StartedAt = %v after the run started", m.Running, m.RunStats.StartedAt)
	}

	m = handle(t, m,
		agentcore.MessageStart{},
		agentcore.ToolStart{Call: call("t1", "read", `{"file_path":"a.go"}`)},
	)
	if !m.IsStream {
		t.Fatal("MessageStart did not start the stream")
	}
	if _, ok := m.PendingTools["t1"]; !ok {
		t.Fatal("ToolStart did not track the call")
	}
	if _, ok := m.ToolHeaders["t1"]; !ok {
		t.Fatal("ToolStart did not buffer the header")
	}

	m = handle(t, m,
		agentcore.ToolEnd{Call: call("t1", "read", `{"file_path":"a.go"}`), Result: agentcore.TextResult("1\tx")},
		agentcore.RunEnd{Reason: agentcore.EndDone, Turns: 2, ToolCalls: 1},
	)
	if len(m.PendingTools) != 0 {
		t.Fatalf("pending tools after ToolEnd: %v", m.PendingTools)
	}
	if m.Running || m.IsStream {
		t.Fatalf("Running = %v, IsStream = %v after RunEnd", m.Running, m.IsStream)
	}
	if !strings.Contains(lastPrinted(m), "2 turns") {
		t.Fatalf("run summary = %q", lastPrinted(m))
	}
}

// The deltas of a response stream into the live area until its MessageEnd
// prints it; a retry discards them.
func TestStreamedResponse(t *testing.T) {
	m := handle(t, eventModel(),
		agentcore.MessageStart{},
		agentcore.MessageDelta{Event: litellm.ReasoningDelta{Text: "hmm "}},
		agentcore.MessageDelta{Event: litellm.ReasoningDelta{Text: "ok"}},
		agentcore.MessageDelta{Event: litellm.TextDelta{Text: "draft"}},
	)
	if m.Thinking.String() != "hmm ok" || m.Streaming.String() != "draft" {
		t.Fatalf("thinking %q, streaming %q", m.Thinking.String(), m.Streaming.String())
	}

	m = handle(t, m, agentcore.Retry{Attempt: 1, MaxRetries: 3})
	if m.IsStream || m.Streaming.Len() != 0 || m.Thinking.Len() != 0 {
		t.Fatal("a retry kept the failed response")
	}

	msg := agentcore.Message{Role: litellm.RoleAssistant, Blocks: []litellm.Block{litellm.ReasoningBlock{Text: "thought"}, litellm.Text("answer")}, Usage: &agentcore.Usage{Input: 10, Output: 5}}
	m = handle(t, m, agentcore.MessageStart{}, agentcore.MessageDelta{Event: litellm.TextDelta{Text: "answer"}}, agentcore.MessageEnd{Message: msg})
	if m.IsStream || m.Streaming.Len() != 0 {
		t.Fatal("MessageEnd left the stream open")
	}
	if got := lastPrinted(m); !strings.Contains(got, "thought") || !strings.Contains(got, "answer") {
		t.Fatalf("printed %q", got)
	}
	if m.RunStats.Input != 10 || m.RunStats.Output != 5 {
		t.Fatalf("usage %d/%d", m.RunStats.Input, m.RunStats.Output)
	}
}

// A previewed edit prints its header and diff at ToolStart, before it is
// approved; its end then adds nothing unless it failed.
func TestEditPreview(t *testing.T) {
	edit := call("t1", "edit", `{"file_path":"file.txt"}`)
	edit.Preview = "-1 old\n+1 new\n"

	m := handle(t, eventModel(), agentcore.ToolStart{Call: edit})
	if got := lastPrinted(m); !strings.Contains(got, "Edit") || !strings.Contains(got, "new") {
		t.Fatalf("preview printed %q", got)
	}
	if _, ok := m.ToolHeaders["t1"]; ok {
		t.Fatal("the previewed header is printed again with the result")
	}
	if _, cmd := m.HandleAgentEvent(agentcore.ToolEnd{Call: edit, Result: agentcore.TextResult("Edited file.txt.\n-1 old\n+1 new\n")}); cmd != nil {
		t.Fatal("the end of a previewed edit printed")
	}

	m = handle(t, eventModel(), agentcore.ToolStart{Call: edit}, agentcore.ToolEnd{Call: edit, Result: agentcore.ErrorResult("boom")})
	if got := lastPrinted(m); !strings.Contains(got, "boom") {
		t.Fatalf("failed edit printed %q", got)
	}

	plain := call("t2", "edit", `{"file_path":"file.txt"}`)
	m = handle(t, eventModel(), agentcore.ToolStart{Call: plain}, agentcore.ToolEnd{Call: plain, Result: agentcore.TextResult("Edited file.txt.\n-1 old\n+1 new\n")})
	if got := lastPrinted(m); !strings.Contains(got, "Edit") || !strings.Contains(got, "new") {
		t.Fatalf("unpreviewed edit printed %q", got)
	}
}

// Bash reports its output lines; a sub-agent its responses and tool calls,
// and what its runs used ends up under its result.
func TestToolProgress(t *testing.T) {
	bash := call("b1", "bash", `{"command":"make"}`)
	m := handle(t, eventModel(),
		agentcore.ToolStart{Call: bash},
		agentcore.ToolUpdate{Call: bash, Progress: "building"},
		agentcore.ToolUpdate{Call: bash, Progress: "done"},
	)
	if got := m.ToolOutputBuf["b1"].String(); got != "building\ndone\n" {
		t.Fatalf("bash output %q", got)
	}

	sub := call("s1", "subagent", `{"agent":"explore","task":"look"}`)
	progress := func(ev agentcore.Event) agentcore.Event {
		return agentcore.ToolUpdate{Call: sub, Progress: subagent.Progress{Spawn: subagent.Spawn{Agent: "explore", ID: "explore#1"}, Event: ev}}
	}
	reply := agentcore.Message{Role: litellm.RoleAssistant, Blocks: []litellm.Block{litellm.ReasoningBlock{Text: "let me\nsee"}, litellm.Text("reading")}, Usage: &agentcore.Usage{Input: 1200, Output: 30}}
	m = handle(t, m,
		agentcore.ToolStart{Call: sub},
		progress(agentcore.MessageDelta{Event: litellm.TextDelta{Text: "rea"}}),
		progress(agentcore.MessageEnd{Message: reply}),
		progress(agentcore.ToolStart{Call: call("r1", "read", `{"file_path":"main.go"}`)}),
		progress(agentcore.RunEnd{Reason: agentcore.EndDone, Turns: 2, ToolCalls: 1}),
	)
	out := ansi.Strip(m.ToolOutputBuf["s1"].String())
	for _, want := range []string{"thinking let me see\n", "reply reading\n", "read main.go\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("sub-agent output %q lacks %q", out, want)
		}
	}

	m = handle(t, m, agentcore.ToolEnd{Call: sub, Result: agentcore.TextResult("found it")})
	if got := lastPrinted(m); !strings.Contains(got, "found it") || !strings.Contains(got, "2 turns · 1 tools · ↑1.2k ↓30 tokens") {
		t.Fatalf("sub-agent result printed %q", got)
	}
	if len(m.SubagentUsage) != 0 {
		t.Fatal("the usage of an ended call is kept")
	}
}
