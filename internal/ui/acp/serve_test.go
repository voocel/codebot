package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agentcore "github.com/voocel/agentcore"
	"github.com/voocel/litellm"
	"github.com/voocel/litellm/litellmtest"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/infra/provider"
	"github.com/voocel/codebot/internal/interact"
)

// client drives a Server over the protocol's stream, as an editor does.
type client struct {
	t     *testing.T
	in    io.Writer
	lines *bufio.Scanner
	id    int
	cwd   string // the project
	model *litellmtest.Provider
}

type message struct {
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// serve boots codebot in mode in a temporary home and git repository, with
// the replies scripted and a skill that forks.
func serve(t *testing.T, mode interact.Mode, replies ...litellmtest.Reply) *client {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := `{"provider":"anthropic","model":"claude-haiku-4-5","snapshot":false,
		"providers":{"anthropic":{"api_key":"test","models":["claude-haiku-4-5","claude-sonnet-4-5"]}}}`
	skill := filepath.Join(home, ".codebot", "skills", "asker")
	if err := os.MkdirAll(skill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codebot", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\ndescription: asks a sub-agent\ncontext: fork\n---\nFind it.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "README.md"), []byte("# demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, cwd, "init", "-q", "-b", "main")
	git(t, cwd, "add", ".")
	git(t, cwd, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")

	model := litellmtest.New(replies...)
	a, err := app.Boot(app.Options{
		Cwd: cwd, Mode: mode,
		NewModel: func(spec provider.ModelSpec) (agentcore.Model, error) {
			c, err := litellm.New(model)
			return agentcore.Model{Client: c, Request: litellm.Request{Model: spec.Model}}, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = NewServer("test").serve(a, inR, outW) }()
	t.Cleanup(func() { inW.Close() })
	lines := bufio.NewScanner(outR)
	lines.Buffer(make([]byte, 1<<20), 1<<24)
	c := &client{t: t, in: inW, lines: lines, cwd: cwd, model: model}
	c.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}})
	return c
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// send sends a request without waiting for its response.
func (c *client) send(method string, params any) int {
	c.t.Helper()
	c.id++
	c.write(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	return c.id
}

func (c *client) notify(method string, params any) {
	c.t.Helper()
	c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *client) write(msg map[string]any) {
	c.t.Helper()
	data, _ := json.Marshal(msg)
	if _, err := fmt.Fprintf(c.in, "%s\n", data); err != nil {
		c.t.Fatal(err)
	}
}

// call sends a request and returns what came back up to its response, the
// response last.
func (c *client) call(method string, params any) []message {
	c.t.Helper()
	return c.until(c.send(method, params))
}

func (c *client) until(id int) []message {
	c.t.Helper()
	var got []message
	for {
		m := c.next()
		if m.Method == "session/request_permission" {
			c.t.Fatalf("the client was asked: %s", m.Params)
		}
		got = append(got, m)
		if m.ID != nil && *m.ID == id && m.Method == "" {
			return got
		}
	}
}

func (c *client) next() message {
	c.t.Helper()
	if !c.lines.Scan() {
		c.t.Fatal("the stream ended")
	}
	var m message
	if err := json.Unmarshal(c.lines.Bytes(), &m); err != nil {
		c.t.Fatal(err)
	}
	return m
}

// open opens a session in cwd with session/new and takes the commands that
// follow.
func (c *client) open(cwd string) string {
	c.t.Helper()
	got := c.call("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(got[len(got)-1].Result, &res); err != nil || res.SessionID == "" {
		c.t.Fatalf("session/new answered %s %s", got[len(got)-1].Result, got[len(got)-1].Error)
	}
	if u := updateOf(c.t, c.next()); u.Update.Kind != "available_commands_update" {
		c.t.Fatalf("session/new was followed by %s", u.Update.Kind)
	}
	return res.SessionID
}

func (c *client) prompt(id string, blocks ...map[string]any) []message {
	c.t.Helper()
	return c.call("session/prompt", map[string]any{"sessionId": id, "prompt": blocks})
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

type update struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		Kind string `json:"sessionUpdate"`
		// A chunk's content is a block; a tool call's, a list of them.
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"toolCallId"`
		Title      string          `json:"title"`
		ToolKind   string          `json:"kind"`
		Status     string          `json:"status"`
		Locations  []struct {
			Path string `json:"path"`
		} `json:"locations"`
		Entries []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"entries"`
		Commands []struct {
			Name  string `json:"name"`
			Input *struct {
				Hint string `json:"hint"`
			} `json:"input"`
		} `json:"availableCommands"`
		CurrentModeID string   `json:"currentModeId"`
		ConfigOptions []option `json:"configOptions"`
	} `json:"update"`
}

type option struct {
	ID           string `json:"id"`
	CurrentValue string `json:"currentValue"`
	Options      []struct {
		Options []struct {
			Value string `json:"value"`
		} `json:"options"`
	} `json:"options"`
}

// diffContent is a diff in an update's content.
type diffContent struct {
	Type    string  `json:"type"`
	Path    string  `json:"path"`
	OldText *string `json:"oldText"`
	NewText string  `json:"newText"`
}

func (u update) diffs() []diffContent {
	var out []diffContent
	_ = json.Unmarshal(u.Update.Content, &out)
	return slices.DeleteFunc(out, func(d diffContent) bool { return d.Type != "diff" })
}

// text is the text of the update's content.
func (u update) text() string {
	var block struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(u.Update.Content, &block) == nil {
		return block.Text
	}
	var list []struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(u.Update.Content, &list)
	var out strings.Builder
	for _, c := range list {
		out.WriteString(c.Content.Text)
	}
	return out.String()
}

func updateOf(t *testing.T, m message) update {
	t.Helper()
	var u update
	if m.Method != "session/update" || json.Unmarshal(m.Params, &u) != nil {
		t.Fatalf("got %s %s, want a session update", m.Method, m.Params)
	}
	return u
}

// updates returns the session updates in msgs, the response left out.
func updates(t *testing.T, msgs []message) []update {
	t.Helper()
	var out []update
	for _, m := range msgs[:len(msgs)-1] {
		out = append(out, updateOf(t, m))
	}
	return out
}

func stopReason(t *testing.T, m message) string {
	t.Helper()
	var res struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatalf("the prompt ended with %s %s", m.Result, m.Error)
	}
	return res.StopReason
}

func value(opts []option, id string) string {
	for _, o := range opts {
		if o.ID == id {
			return o.CurrentValue
		}
	}
	return ""
}

// The skills the user can invoke follow the response that opens the
// session, since the client drops updates for a session it has yet to hear
// of.
func TestTheCommandsFollowTheNewSession(t *testing.T) {
	c := serve(t, interact.ModeBalanced)
	if got := c.call("session/new", map[string]any{"cwd": c.cwd, "mcpServers": []any{}}); len(got) != 1 {
		t.Fatalf("session/new sent %d messages, want the response alone", len(got))
	}
	u := updateOf(t, c.next())
	hints := map[string]string{}
	for _, cmd := range u.Update.Commands {
		hints[cmd.Name] = ""
		if cmd.Input != nil {
			hints[cmd.Name] = cmd.Input.Hint
		}
	}
	if u.Update.Kind != "available_commands_update" || hints["init"] != "[focus]" || hints["review"] != "[scope]" {
		t.Fatalf("got %s with %v, want the skills with their hints", u.Update.Kind, hints)
	}
	if _, ok := hints["asker"]; !ok {
		t.Fatalf("the commands %v leave out a skill of the user's", hints)
	}
}

// Each session/new is a conversation of its own, with its own mode.
func TestSessionsStayApart(t *testing.T) {
	c := serve(t, interact.ModeBalanced, litellmtest.Text("one"), litellmtest.Text("two"))
	a, b := c.open(c.cwd), c.open(c.cwd)
	if a == b {
		t.Fatalf("both sessions are %s", a)
	}
	c.prompt(a, text("first"))
	c.prompt(b, text("second"))
	if got := c.model.Requests()[1].Messages; strings.Contains(fmt.Sprint(got), "first") {
		t.Errorf("the second session's request holds the first one's prompt: %v", got)
	}

	got := c.call("session/set_config_option", map[string]any{"sessionId": a, "configId": "mode", "value": "trust"})
	var res struct {
		ConfigOptions []option `json:"configOptions"`
	}
	if err := json.Unmarshal(got[len(got)-1].Result, &res); err != nil || value(res.ConfigOptions, "mode") != "trust" {
		t.Fatalf("set_config_option answered %s %s", got[len(got)-1].Result, got[len(got)-1].Error)
	}
	kinds := []string{}
	for _, u := range updates(t, got) {
		if u.SessionID != a {
			t.Errorf("session %s heard of the other's mode", u.SessionID)
		}
		kinds = append(kinds, u.Update.Kind)
	}
	if !slices.Equal(kinds, []string{"current_mode_update", "config_option_update"}) {
		t.Errorf("the mode change sent %v, want it in both places", kinds)
	}

	got = c.call("session/set_config_option", map[string]any{"sessionId": a, "configId": "model", "value": "anthropic/claude-sonnet-4-5"})
	if err := json.Unmarshal(got[len(got)-1].Result, &res); err != nil || value(res.ConfigOptions, "model") != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("set_config_option answered %s %s", got[len(got)-1].Result, got[len(got)-1].Error)
	}

	loaded := c.call("session/resume", map[string]any{"sessionId": b, "cwd": c.cwd})
	if err := json.Unmarshal(loaded[len(loaded)-1].Result, &res); err != nil || value(res.ConfigOptions, "mode") != "balanced" || value(res.ConfigOptions, "model") != "anthropic/claude-haiku-4-5" {
		t.Fatalf("the other session answered %s", loaded[len(loaded)-1].Result)
	}
}

// The model a session runs is among the choices even when its provider
// doesn't list it.
func TestTheCurrentModelIsAChoice(t *testing.T) {
	got := models(map[string]config.ProviderConfig{"anthropic": {Models: []string{"claude-sonnet-4-5"}}, "openai": {}}, "anthropic", "claude-opus-4-1")
	if len(got) != 1 || len(got[0].Options) != 2 || got[0].Options[1].Value != "anthropic/claude-opus-4-1" {
		t.Fatalf("choices %+v", got)
	}
}

// A session runs where the client says, so in a linked worktree of the
// project too, whose files it edits like the project's; a folder of another
// project is refused.
func TestASessionInAWorktree(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"file_path": "notes.md", "content": "hi"})
	c := serve(t, interact.ModeAcceptEdits, litellmtest.Respond(litellm.ToolUseBlock{ID: "w1", Name: "write", Arguments: string(args)}), litellmtest.Text("done"))
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, c.cwd, "worktree", "add", "-q", "-b", "wt", wt)

	id := c.open(wt)
	got := c.prompt(id, text("take notes"))
	if reason := stopReason(t, got[len(got)-1]); reason != "end_turn" {
		t.Fatalf("the turn ended with %s", reason)
	}
	for _, u := range updates(t, got) {
		switch u.Update.Kind {
		case "tool_call":
			if d := u.diffs(); len(d) != 1 || d[0].Path != filepath.Join(wt, "notes.md") || d[0].OldText != nil || d[0].NewText != "hi" {
				t.Errorf("the write showed %s", u.Update.Content)
			}
		case "tool_call_update":
			if u.Update.Status != "completed" || u.Update.Content != nil {
				t.Errorf("the write ended with %s %s, which replaces its diff", u.Update.Status, u.Update.Content)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(wt, "notes.md")); err != nil || string(data) != "hi" {
		t.Fatalf("the worktree's notes.md = %q, %v", data, err)
	}

	got = c.call("session/new", map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}})
	if got[len(got)-1].Error == nil {
		t.Fatal("a session outside the project opened")
	}
}

// A link to a file stays where the user put it; an embedded file's text
// follows the prompt.
func TestPromptContent(t *testing.T) {
	c := serve(t, interact.ModeBalanced)
	id := c.open(c.cwd)
	uri := "file://" + filepath.Join(c.cwd, "README.md")
	c.prompt(id,
		text("explain "),
		map[string]any{"type": "resource_link", "uri": uri, "name": "README.md"},
		text(" with "),
		map[string]any{"type": "resource", "resource": map[string]any{"uri": uri, "text": "# demo"}},
	)
	want := "explain [@README.md](" + uri + ") with [@README.md](" + uri + ")\n<context ref=\"" + uri + "\">\n# demo\n</context>"
	for _, m := range c.model.Requests()[0].Messages {
		if got := (agentcore.Message{Role: m.Role, Blocks: m.Blocks}).Text(); strings.HasPrefix(got, "explain") {
			if got != want {
				t.Fatalf("the model got %q\nwant %q", got, want)
			}
			return
		}
	}
	t.Fatal("the prompt did not reach the model")
}

// Loading a session sends the conversation so far before answering, and
// the commands after; listing finds it.
func TestLoadAndListSessions(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"file_path": "README.md"})
	todos, _ := json.Marshal(map[string]any{"todos": []map[string]string{{"content": "Read it", "status": "in_progress"}}})
	c := serve(t, interact.ModeBalanced,
		litellmtest.Respond(litellm.ToolUseBlock{ID: "t1", Name: "todo_write", Arguments: string(todos)}, litellm.ToolUseBlock{ID: "r1", Name: "read", Arguments: string(args)}),
		litellmtest.Text("a demo"))
	id := c.open(c.cwd)
	live := updates(t, c.prompt(id, text("what is this")))
	var calls []update
	for _, u := range live {
		switch u.Update.Kind {
		case "plan":
			if len(u.Update.Entries) != 1 || u.Update.Entries[0].Content != "Read it" {
				t.Errorf("plan %+v", u.Update.Entries)
			}
		case "tool_call", "tool_call_update":
			calls = append(calls, u)
		}
	}
	if len(calls) != 2 || calls[0].Update.Title != "Read README.md" || calls[0].Update.ToolKind != "read" ||
		len(calls[0].Update.Locations) != 1 || calls[0].Update.Locations[0].Path != filepath.Join(c.cwd, "README.md") ||
		calls[1].Update.Status != "completed" || !strings.Contains(calls[1].text(), "# demo") {
		t.Fatalf("the read showed as %+v", calls)
	}

	c.call("session/close", map[string]any{"sessionId": id})
	listed := c.call("session/list", map[string]any{"cwd": c.cwd})
	var list struct {
		Sessions []struct {
			SessionID string `json:"sessionId"`
			Title     string `json:"title"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(listed[0].Result, &list); err != nil || len(list.Sessions) != 1 || list.Sessions[0].SessionID != id || list.Sessions[0].Title != "what is this" {
		t.Fatalf("session/list answered %s", listed[0].Result)
	}

	loaded := c.call("session/load", map[string]any{"sessionId": id, "cwd": c.cwd, "mcpServers": []any{}})
	var kinds []string
	for _, u := range updates(t, loaded) {
		kinds = append(kinds, u.Update.Kind+":"+u.text()+u.Update.Title)
	}
	want := []string{"user_message_chunk:what is this", "tool_call:Read README.md", "plan:", "tool_call_update:", "agent_message_chunk:a demo"}
	if len(kinds) != len(want) {
		t.Fatalf("load replayed %q, want %q", kinds, want)
	}
	for i := range want {
		if !strings.HasPrefix(kinds[i], want[i]) {
			t.Fatalf("load replayed %q, want %q", kinds, want)
		}
	}
	if u := updateOf(t, c.next()); u.Update.Kind != "available_commands_update" {
		t.Fatalf("the load was followed by %s", u.Update.Kind)
	}
}

// A skill that forks answers in a message and ends the turn, whatever the
// conversation's last run did.
func TestASkillThatForksEndsItsTurn(t *testing.T) {
	refused := &litellm.Error{Type: litellm.ErrorTypeValidation, Message: "unknown model", Provider: "anthropic", StatusCode: 400}
	c := serve(t, interact.ModeBalanced, litellmtest.Fail(refused), litellmtest.Text("found it"))
	id := c.open(c.cwd)
	if got := c.prompt(id, text("hi")); got[len(got)-1].Error == nil {
		t.Fatal("the refused run ended its turn")
	}
	got := c.prompt(id, text("/asker where"))
	if reason := stopReason(t, got[len(got)-1]); reason != "end_turn" {
		t.Fatalf("the turn ended with %s", reason)
	}
	if len(got) != 2 || updateOf(t, got[0]).text() != "found it" {
		t.Fatalf("the turn sent %v, want the answer, then the response", got)
	}
}

// A prompt sent while the last one runs supersedes it: the last one is
// cancelled, and the new one runs on its own.
func TestANewPromptSupersedesTheLast(t *testing.T) {
	c := serve(t, interact.ModeBalanced, litellmtest.Reply{Stall: true}, litellmtest.Text("second"))
	id := c.open(c.cwd)
	first := c.send("session/prompt", map[string]any{"sessionId": id, "prompt": []any{text("first")}})
	for len(c.model.Requests()) == 0 {
		time.Sleep(time.Millisecond)
	}
	second := c.send("session/prompt", map[string]any{"sessionId": id, "prompt": []any{text("second")}})
	answers := map[int]message{}
	var said strings.Builder
	for len(answers) < 2 {
		m := c.next()
		switch {
		case m.ID != nil && m.Method == "":
			answers[*m.ID] = m
		case m.Method == "session/update":
			if u := updateOf(t, m); u.Update.Kind == "agent_message_chunk" {
				said.WriteString(u.text())
			}
		}
	}
	if reason := stopReason(t, answers[first]); reason != "cancelled" {
		t.Errorf("the first prompt ended with %s", reason)
	}
	if reason := stopReason(t, answers[second]); reason != "end_turn" || said.String() != "second" {
		t.Errorf("the second prompt ended with %s, saying %q", reason, said.String())
	}
}

// Cancelling or closing the session stops a skill that forks too, which runs
// outside the conversation's runs.
func TestCancelAndCloseStopASkillThatForks(t *testing.T) {
	for _, method := range []string{"session/cancel", "session/close"} {
		c := serve(t, interact.ModeBalanced, litellmtest.Reply{Stall: true})
		id := c.open(c.cwd)
		req := c.send("session/prompt", map[string]any{"sessionId": id, "prompt": []any{text("/asker where")}})
		for len(c.model.Requests()) == 0 {
			time.Sleep(time.Millisecond) // the sub-agent's request has to be on its way
		}
		if method == "session/cancel" {
			c.notify(method, map[string]any{"sessionId": id})
		} else {
			c.send(method, map[string]any{"sessionId": id})
		}
		got := c.until(req)
		if reason := stopReason(t, got[len(got)-1]); reason != "cancelled" {
			t.Fatalf("%s: the turn ended with %s", method, reason)
		}
	}
}
