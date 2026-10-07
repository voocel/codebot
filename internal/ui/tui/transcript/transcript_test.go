package transcript

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/litellm"
)

func render(t *Transcript, p Params) []string {
	var out []string
	for _, c := range t.Cells() {
		out = append(out, c.Render(p)...)
		out = append(out, "--")
	}
	return out
}

func plain(lines []string) string { return ansi.Strip(strings.Join(lines, "\n")) }

func assistant(text string, calls ...litellm.ToolUseBlock) agentcore.Message {
	m := agentcore.Message{Role: litellm.RoleAssistant}
	if text != "" {
		m.Blocks = append(m.Blocks, litellm.Text(text))
	}
	for _, c := range calls {
		m.Blocks = append(m.Blocks, c)
	}
	return m
}

func stream(t *Transcript, m agentcore.Message, chunks ...string) {
	t.Apply(agentcore.MessageStart{})
	for _, c := range chunks {
		t.Apply(agentcore.MessageDelta{Event: litellm.TextDelta{Text: c}})
	}
	t.Apply(agentcore.MessageEnd{Message: m})
}

func call(id, name, args string) litellm.ToolUseBlock {
	return litellm.ToolUseBlock{ID: id, Name: name, Arguments: args}
}

// The package's core invariant: a run renders the same live as restored
// from the history it wrote.
func TestApplyMatchesLoad(t *testing.T) {
	user := agentcore.UserText("fix the tests")
	first := assistant("Let me look at the **failures**.", call("c1", "bash", `{"command":"go test ./..."}`), call("c2", "todo_write", `{"todos":[]}`), call("c3", "edit", `{"file_path":"/x/a.go"}`), call("c4", "write", `{"file_path":"/x/b.go","content":"package x\n\nvar B = 2\n"}`))
	r1 := agentcore.ToolResult("c1", agentcore.TextResult("--- FAIL: TestX\nFAIL\n[exit code 1]"))
	r2 := agentcore.ToolResult("c2", agentcore.TextResult("ok"))
	r3 := agentcore.ToolResult("c3", agentcore.TextResult("Edited /x/a.go.\n- 3 return 1\n+ 3 return 2\n"))
	r4 := agentcore.ToolResult("c4", agentcore.TextResult("Overwrote /x/b.go (22 bytes)."))
	final := assistant("Fixed: `X` now returns 2.\n\n- one\n- two")
	history := []agentcore.Message{user, first, r1, r2, r3, r4, final}

	live := New()
	live.Apply(agentcore.MessageEnd{Message: user})
	stream(live, first, "Let me look ", "at the **failures**.")
	// Parallel calls start in any order; calls awaiting approval show a
	// preview.
	for _, c := range slices.Backward(first.ToolCalls()) {
		live.Apply(agentcore.ToolStart{Call: agentcore.ToolCall{ID: c.ID, Name: c.Name, Args: json.RawMessage(c.Arguments), Preview: "-1 var B = 1\n+1 var B = 2\n"}})
	}
	live.Apply(agentcore.ToolUpdate{Call: agentcore.ToolCall{ID: "c1"}, Progress: "--- FAIL: TestX"})
	for _, r := range []agentcore.Message{r1, r2, r3, r4} {
		res, _ := r.ToolResult()
		live.Apply(agentcore.ToolEnd{Call: agentcore.ToolCall{ID: res.ToolUseID}, Result: agentcore.Result{Content: res.Content, IsError: res.IsError}})
		live.Apply(agentcore.MessageEnd{Message: r})
	}
	stream(live, final, "Fixed: `X` now ", "returns 2.\n\n- one\n- two")
	live.Apply(agentcore.RunEnd{Reason: agentcore.EndDone})

	for _, p := range []Params{{Width: 60}, {Width: 30, Expanded: true}} {
		got, want := plain(render(live, p)), plain(render(Load(history), p))
		if got != want {
			t.Errorf("width %d: live\n%s\nrestored\n%s", p.Width, got, want)
		}
	}
	if got := plain(render(live, Params{Width: 60})); strings.Contains(got, "todo") {
		t.Errorf("the todo_write call shows:\n%s", got)
	}
}

func TestRenderFitsWidth(t *testing.T) {
	tr := Load([]agentcore.Message{
		agentcore.UserText(strings.Repeat("a long prompt ", 20)),
		assistant(strings.Repeat("word ", 50), call("c1", "bash", `{"command":"`+strings.Repeat("x", 200)+`"}`)),
		agentcore.ToolResult("c1", agentcore.TextResult(strings.Repeat("output ", 40))),
	})
	tr.Append(Fail(strings.Repeat("error ", 30)))
	for _, width := range []int{12, 40, 100} {
		for _, line := range render(tr, Params{Width: width}) {
			if w := ansi.StringWidth(line); w > width {
				t.Errorf("width %d: %d wide: %q", width, w, ansi.Strip(line))
			}
		}
	}
}

func TestRetryDiscardsTheResponse(t *testing.T) {
	tr := New()
	tr.Apply(agentcore.MessageStart{})
	tr.Apply(agentcore.MessageDelta{Event: litellm.TextDelta{Text: "half"}})
	tr.Apply(agentcore.Retry{Attempt: 2})
	stream(tr, assistant("whole"), "whole")
	if got := plain(render(tr, Params{Width: 40})); strings.Contains(got, "half") || !strings.Contains(got, "whole") {
		t.Errorf("got\n%s", got)
	}
}

func TestInterruptedRun(t *testing.T) {
	tr := New()
	tr.Apply(agentcore.ToolStart{Call: agentcore.ToolCall{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"sleep 9"}`)}})
	if c := tr.Cells()[0]; !c.Live() {
		t.Fatal("a running call should animate")
	}
	tr.Apply(agentcore.RunEnd{Reason: agentcore.EndAborted, Err: context.Canceled})
	got := plain(render(tr, Params{Width: 40}))
	if !strings.Contains(got, "Interrupted") || tr.Cells()[0].Live() {
		t.Errorf("got\n%s", got)
	}
}

func TestStreamingRendersStablePrefixOnce(t *testing.T) {
	c := &Assistant{streaming: true}
	text := "# Head\n\nfirst paragraph\n\n```go\ncode\n\nmore\n```\n\ntail being typed"
	c.text.WriteString(text)
	streamed := plain(c.Render(Params{Width: 40}))
	c.streaming = false
	whole := plain(c.Render(Params{Width: 40}))
	if streamed != whole {
		t.Errorf("streamed\n%s\nwhole\n%s", streamed, whole)
	}
	if cut := stableCut(text); !strings.HasPrefix(text[cut:], "tail") {
		t.Errorf("cut inside the fence: %q", text[cut:])
	}
}

func TestSubagentProgress(t *testing.T) {
	tr := New()
	tr.Apply(agentcore.ToolStart{Call: agentcore.ToolCall{ID: "c1", Name: "subagent", Args: json.RawMessage(`{"agent":"explore","task":"find the bug"}`)}})
	spawn := subagent.Spawn{Agent: "explore", ID: "explore#1"}
	for _, ev := range []agentcore.Event{
		agentcore.ToolStart{Call: agentcore.ToolCall{ID: "s1", Name: "read", Args: json.RawMessage(`{"file_path":"main.go"}`)}},
	} {
		tr.Apply(agentcore.ToolUpdate{Call: agentcore.ToolCall{ID: "c1"}, Progress: subagent.Progress{Spawn: spawn, Event: ev}})
	}
	got := plain(render(tr, Params{Width: 80, Now: time.Now()}))
	for _, want := range []string{"Explore(find the bug)", "explore#1 · 1 tools", "Read(main.go)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	tool := tr.Cells()[0].(*Tool)
	if len(tool.Agents()) != 1 || len(tool.Agents()[0].Transcript.Cells()) != 1 {
		t.Errorf("the sub-agent's own transcript was not kept")
	}
}

// A call waiting for approval neither spins nor counts the wait; it counts
// from when it runs, and shows no empty output before any.
// A call made after another that runs alone waits its turn: it shows from
// the reply on, without a spinner or a time until it starts.
func TestACallWaitsItsTurn(t *testing.T) {
	tr := New()
	start := time.Now()
	tr.now = func() time.Time { return start }
	tr.Apply(agentcore.MessageEnd{Message: assistant("", call("c1", "bash", `{"command":"make"}`), call("c2", "bash", `{"command":"git log"}`))})
	tr.Apply(agentcore.ToolStart{Call: agentcore.ToolCall{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"make"}`)}})
	got := plain(render(tr, Params{Width: 60, Now: start.Add(28 * time.Second)}))
	if !strings.Contains(got, "Bash(make) · 28s") || !strings.Contains(got, "○ Bash(git log)\n") {
		t.Fatalf("queued:\n%s", got)
	}

	tr.now = func() time.Time { return start.Add(28 * time.Second) }
	tr.Apply(agentcore.ToolStart{Call: agentcore.ToolCall{ID: "c2", Name: "bash", Args: json.RawMessage(`{"command":"git log"}`)}})
	got = plain(render(tr, Params{Width: 60, Now: start.Add(31 * time.Second)}))
	if strings.Contains(got, "○") || !strings.Contains(got, "Bash(git log) · 3s") {
		t.Fatalf("started:\n%s", got)
	}
}

func TestAToolWaitsForApproval(t *testing.T) {
	tr := New()
	start := time.Now()
	tr.now = func() time.Time { return start }
	tr.Apply(agentcore.ToolStart{Call: agentcore.ToolCall{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"git diff"}`)}})
	tr.Wait("c1", true)
	got := plain(render(tr, Params{Width: 60, Now: start.Add(26 * time.Minute)}))
	if !strings.Contains(got, "○ Bash(git diff) · waiting for approval") || strings.Contains(got, "26m") || strings.Contains(got, "⎿") {
		t.Fatalf("waiting:\n%s", got)
	}

	tr.now = func() time.Time { return start.Add(26 * time.Minute) }
	tr.Wait("c1", false)
	got = plain(render(tr, Params{Width: 60, Now: start.Add(26*time.Minute + 5*time.Second)}))
	if !strings.Contains(got, "Bash(git diff) · 5s") || strings.Contains(got, "waiting") {
		t.Fatalf("running:\n%s", got)
	}

	// A subagent's call waits inside the call that runs it, which redraws.
	tr.Apply(agentcore.ToolStart{Call: agentcore.ToolCall{ID: "c2", Name: "subagent", Args: json.RawMessage(`{"agent":"explore","task":"look"}`)}})
	spawn := subagent.Spawn{Agent: "explore", ID: "explore#1"}
	start2 := agentcore.ToolStart{Call: agentcore.ToolCall{ID: "s1", Name: "bash", Args: json.RawMessage(`{"command":"make"}`)}}
	tr.Apply(agentcore.ToolUpdate{Call: agentcore.ToolCall{ID: "c2"}, Progress: subagent.Progress{Spawn: spawn, Event: start2}})
	parent := tr.Cells()[1].(*Tool)
	before := parent.Version()
	tr.Wait("s1", true)
	if !parent.Agents()[0].Transcript.Cells()[0].(*Tool).Waiting || parent.Version() == before {
		t.Error("the subagent's call does not wait, or its parent does not redraw")
	}
}

func TestHomePath(t *testing.T) {
	for p, want := range map[string]string{
		home:                          "~",
		filepath.Join(home, "x", "y"): filepath.Join("~", "x", "y"),
		home + "x":                    home + "x",
		"/elsewhere":                  "/elsewhere",
	} {
		if got := HomePath(p); got != want {
			t.Errorf("HomePath(%q) = %q, want %q", p, got, want)
		}
	}
}
